// Package auth: Passwörter (argon2id), Benutzer, Sessions, Einladungen und Rate-Limit.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/Buffer379x/smmyp/internal/store"
)

const (
	SessionTTL    = 30 * 24 * time.Hour
	InviteTTL     = 7 * 24 * time.Hour
	MinPassword   = 10
	argonTime     = 2
	argonMemoryKB = 19 * 1024 // OWASP-Empfehlung; hält das RAM-Budget ein
	argonThreads  = 1
)

var (
	ErrInvalidLogin = errors.New("Benutzername oder Passwort falsch")
	ErrDisabled     = errors.New("Konto deaktiviert")
	usernameRe      = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,32}$`)
)

// HashPassword erzeugt einen argon2id-Hash im PHC-Format.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKB, argonThreads, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword prüft in konstanter Zeit.
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash gleicht die Laufzeit bei unbekannten Benutzernamen an (kein Rückschluss auf existierende Konten).
var dummyHash, _ = HashPassword("smmyp-dummy-password")

type Settings struct {
	Theme string `json:"theme,omitempty"` // light | dark | "" (System)
	View  string `json:"view,omitempty"`  // cards | list
	ML    bool   `json:"ml,omitempty"`    // KI-Hilfe (Auto-Tagging, Machine Learning); Standard: aus
}

type User struct {
	ID         int64
	Username   string
	Role       string
	Settings   Settings
	CreatedAt  int64
	DisabledAt int64
}

func (u User) Admin() bool        { return u.Role == "admin" }
func (u User) Scope() store.Scope { return store.Scope{UserID: u.ID, Admin: u.Admin()} }

type Service struct {
	DB *store.DB
}

func token() (raw string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b)
	raw = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:]
}

func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

type ValidationError map[string]string

func (v ValidationError) Error() string { return "Eingaben prüfen" }

func validateCredentials(username, password string) ValidationError {
	verr := ValidationError{}
	if !usernameRe.MatchString(username) {
		verr["username"] = "2–32 Zeichen: Buchstaben, Ziffern, Punkt, Bindestrich, Unterstrich."
	}
	if len([]rune(password)) < MinPassword {
		verr["password"] = fmt.Sprintf("Mindestens %d Zeichen.", MinPassword)
	} else if len(password) > 256 {
		verr["password"] = "Höchstens 256 Zeichen."
	}
	if len(verr) == 0 {
		return nil
	}
	return verr
}

// UserCount: 0 bedeutet „Erst-Setup nötig".
func (s *Service) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, err
}

// CreateUser legt ein Konto an. Rolle: admin | member.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (int64, error) {
	username = strings.TrimSpace(username)
	if verr := validateCredentials(username, password); verr != nil {
		return 0, verr
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return s.insertUser(ctx, tx, username, hash, role, &id)
	})
	return id, err
}

func (s *Service) insertUser(ctx context.Context, tx *sql.Tx, username, hash, role string, id *int64) error {
	res, err := tx.ExecContext(ctx, "INSERT INTO users(username, password_hash, role, created_at) VALUES (?,?,?,?)", username, hash, role, store.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ValidationError{"username": "Benutzername ist vergeben."}
		}
		return err
	}
	*id, err = res.LastInsertId()
	return err
}

// CreateFirstAdmin legt den ersten Admin an, aber nur, solange es noch keinen Benutzer gibt.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if verr := validateCredentials(username, password); verr != nil {
		return 0, verr
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("Setup ist bereits abgeschlossen")
		}
		return s.insertUser(ctx, tx, username, hash, "admin", &id)
	})
	return id, err
}

const userCols = "id, username, role, settings, created_at, ifnull(disabled_at,0)"

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var settings string
	err := row.Scan(&u.ID, &u.Username, &u.Role, &settings, &u.CreatedAt, &u.DisabledAt)
	if err == nil {
		json.Unmarshal([]byte(settings), &u.Settings)
	}
	return u, err
}

// Authenticate prüft Benutzername und Passwort.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	var hash sql.NullString
	u, err := scanUser(s.DB.R.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", strings.TrimSpace(username)))
	if err == nil {
		err = s.DB.R.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", u.ID).Scan(&hash)
	}
	if err != nil || !hash.Valid {
		VerifyPassword(dummyHash, password)
		return User{}, ErrInvalidLogin
	}
	if !VerifyPassword(hash.String, password) {
		return User{}, ErrInvalidLogin
	}
	if u.DisabledAt != 0 {
		return User{}, ErrDisabled
	}
	return u, nil
}

// NewSession erzeugt ein Session-Token; in der DB liegt nur dessen Hash.
func (s *Service) NewSession(ctx context.Context, userID int64, userAgent string) (string, error) {
	raw, hash := token()
	now := store.Now()
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions(token_hash, user_id, created_at, expires_at, user_agent) VALUES (?,?,?,?,?)",
			hash, userID, now.Unix(), now.Add(SessionTTL).Unix(), userAgent)
		return err
	})
	return raw, err
}

// SessionUser liefert den Benutzer einer gültigen Session und verlängert sie höchstens einmal täglich.
func (s *Service) SessionUser(ctx context.Context, raw string) (User, error) {
	if raw == "" || len(raw) > 100 {
		return User{}, store.ErrNotFound
	}
	h := hashToken(raw)
	var u User
	var settings string
	var exp int64
	err := s.DB.R.QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.settings, u.created_at, ifnull(u.disabled_at,0), s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, h).
		Scan(&u.ID, &u.Username, &u.Role, &settings, &u.CreatedAt, &u.DisabledAt, &exp)
	if err != nil {
		return User{}, store.ErrNotFound
	}
	json.Unmarshal([]byte(settings), &u.Settings)
	now := store.Now()
	if exp < now.Unix() || u.DisabledAt != 0 {
		return User{}, store.ErrNotFound
	}
	if newExp := now.Add(SessionTTL).Unix(); newExp-exp > 86400 {
		s.DB.Tx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE token_hash = ?", newExp, h)
			return err
		})
	}
	return u, nil
}

// DeleteSession meldet ab.
func (s *Service) DeleteSession(ctx context.Context, raw string) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hashToken(raw))
		return err
	})
}

// DeleteSessions meldet einen Benutzer überall ab.
func (s *Service) DeleteSessions(ctx context.Context, userID int64) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
		return err
	})
}

// CleanupSessions entfernt abgelaufene Sessions und Einladungen.
func (s *Service) CleanupSessions(ctx context.Context) error {
	now := store.Now().Unix()
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM invites WHERE expires_at < ? AND used_by IS NULL", now)
		return err
	})
}

// CreateInvite erzeugt einen einmaligen Einladungslink-Token (nur Admins).
func (s *Service) CreateInvite(ctx context.Context, sc store.Scope, role string) (string, error) {
	if !sc.Admin {
		return "", store.ErrNotFound
	}
	if role != "admin" {
		role = "member"
	}
	raw, hash := token()
	now := store.Now()
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO invites(token_hash, created_by, role, created_at, expires_at) VALUES (?,?,?,?,?)",
			hash, sc.UserID, role, now.Unix(), now.Add(InviteTTL).Unix())
		return err
	})
	return raw, err
}

// InviteValid prüft, ob ein Einladungstoken noch einlösbar ist.
func (s *Service) InviteValid(ctx context.Context, raw string) bool {
	var n int
	s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM invites WHERE token_hash = ? AND used_by IS NULL AND expires_at >= ?", hashToken(raw), store.Now().Unix()).Scan(&n)
	return n == 1
}

// AcceptInvite löst eine Einladung ein und legt das Konto an (atomar, nur einmal möglich).
func (s *Service) AcceptInvite(ctx context.Context, raw, username, password string) (int64, error) {
	username = strings.TrimSpace(username)
	if verr := validateCredentials(username, password); verr != nil {
		return 0, verr
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var role string
		err := tx.QueryRowContext(ctx, "SELECT role FROM invites WHERE token_hash = ? AND used_by IS NULL AND expires_at >= ?", hashToken(raw), store.Now().Unix()).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		} else if err != nil {
			return err
		}
		if err := s.insertUser(ctx, tx, username, hash, role, &id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE invites SET used_by = ? WHERE token_hash = ?", id, hashToken(raw))
		return err
	})
	return id, err
}

// ChangePassword setzt ein neues Passwort, wenn das aktuelle stimmt.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	var hash sql.NullString
	if err := s.DB.R.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", userID).Scan(&hash); err != nil {
		return err
	}
	if !hash.Valid || !VerifyPassword(hash.String, current) {
		return ValidationError{"current": "Aktuelles Passwort stimmt nicht."}
	}
	return s.SetPassword(ctx, userID, next)
}

// SetPassword setzt ein Passwort ohne Prüfung des alten (CLI `smmyp passwd`).
func (s *Service) SetPassword(ctx context.Context, userID int64, next string) error {
	if len([]rune(next)) < MinPassword {
		return ValidationError{"password": fmt.Sprintf("Mindestens %d Zeichen.", MinPassword)}
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", h, userID)
		return err
	})
}

// ProxyUser liefert (oder legt an) einen Benutzer, der sich über den Reverse-Proxy-Header anmeldet.
// Der erste Benutzer einer leeren Instanz wird Admin. Proxy-Benutzer haben kein Passwort.
func (s *Service) ProxyUser(ctx context.Context, username string, create bool) (User, error) {
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return User{}, ValidationError{"username": "ungültiger Benutzername im Proxy-Header"}
	}
	u, err := s.UserByName(ctx, username)
	if err == nil {
		if u.DisabledAt != 0 {
			return User{}, ErrDisabled
		}
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) || !create {
		return User{}, err
	}
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
			return err
		}
		role := "member"
		if n == 0 {
			role = "admin"
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO users(username, password_hash, role, created_at) VALUES (?, NULL, ?, ?)", username, role, store.Now().Unix())
		return err
	})
	if err != nil {
		return User{}, err
	}
	return s.UserByName(ctx, username)
}

// UserByName (für die CLI).
func (s *Service) UserByName(ctx context.Context, username string) (User, error) {
	u, err := scanUser(s.DB.R.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", username))
	if errors.Is(err, sql.ErrNoRows) {
		return u, store.ErrNotFound
	}
	return u, err
}

// SaveSettings speichert Theme und Ansicht.
func (s *Service) SaveSettings(ctx context.Context, userID int64, st Settings) error {
	b, _ := json.Marshal(st)
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE users SET settings = ? WHERE id = ?", string(b), userID)
		return err
	})
}

// Users listet alle Konten (nur Admins).
func (s *Service) Users(ctx context.Context, sc store.Scope) ([]User, error) {
	if !sc.Admin {
		return nil, store.ErrNotFound
	}
	rows, err := s.DB.R.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetDisabled sperrt oder entsperrt ein Konto; Sperren beendet alle Sessions. Admins können sich nicht selbst sperren.
func (s *Service) SetDisabled(ctx context.Context, sc store.Scope, userID int64, disabled bool) error {
	if !sc.Admin || userID == sc.UserID {
		return store.ErrNotFound
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var v any
		if disabled {
			v = store.Now().Unix()
		}
		res, err := tx.ExecContext(ctx, "UPDATE users SET disabled_at = ? WHERE id = ?", v, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		if disabled {
			_, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
		}
		return err
	})
}
