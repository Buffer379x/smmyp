package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Buffer379x/smmyp/internal/store"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("korrekt-pferd-batterie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("Format: %s", h)
	}
	if !VerifyPassword(h, "korrekt-pferd-batterie") || VerifyPassword(h, "falsch") || VerifyPassword("kaputt", "x") {
		t.Fatal("Verify falsch")
	}
	h2, _ := HashPassword("korrekt-pferd-batterie")
	if h == h2 {
		t.Fatal("Salt fehlt")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if b, _ := l.Blocked("k"); b {
			t.Fatalf("zu früh gesperrt nach %d", i)
		}
		l.Fail("k")
	}
	if b, wait := l.Blocked("k"); !b || wait != time.Minute {
		t.Fatalf("erwartet gesperrt für 1 min, got %v %v", b, wait)
	}
	now = now.Add(61 * time.Second)
	if b, _ := l.Blocked("k"); b {
		t.Fatal("Sperre läuft nicht ab")
	}
	l.Fail("k")
	l.Reset("k")
	if b, _ := l.Blocked("k"); b {
		t.Fatal("Reset wirkt nicht")
	}
}

func TestSessionsAndInvites(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{DB: db}
	if _, err := s.CreateUser(ctx, "x", "kurz", "member"); err == nil {
		t.Fatal("ungültige Eingaben angenommen")
	}
	id, err := s.CreateFirstAdmin(ctx, "chef", "langes-passwort")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFirstAdmin(ctx, "zweiter", "langes-passwort"); err == nil {
		t.Fatal("zweiter Erst-Admin möglich")
	}
	if _, err := s.Authenticate(ctx, "CHEF", "langes-passwort"); err != nil {
		t.Fatalf("Benutzername muss ohne Groß-/Kleinschreibung funktionieren: %v", err)
	}
	if _, err := s.Authenticate(ctx, "niemand", "x"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatal("unbekannter Benutzer")
	}
	tok, _ := s.NewSession(ctx, id, "ua")
	if u, err := s.SessionUser(ctx, tok); err != nil || u.ID != id {
		t.Fatalf("Session: %v", err)
	}
	real := store.Now
	defer func() { store.Now = real }()
	store.Now = func() time.Time { return real().Add(SessionTTL + time.Hour) }
	if _, err := s.SessionUser(ctx, tok); err == nil {
		t.Fatal("abgelaufene Session gültig")
	}
	store.Now = real
	if err := s.ChangePassword(ctx, id, "falsch", "neues-passwort"); err == nil {
		t.Fatal("Passwortwechsel ohne korrektes altes Passwort")
	}
	if err := s.ChangePassword(ctx, id, "langes-passwort", "neues-passwort"); err != nil {
		t.Fatal(err)
	}
	inv, err := s.CreateInvite(ctx, store.Scope{UserID: id, Admin: true}, "member")
	if err != nil || !s.InviteValid(ctx, inv) {
		t.Fatalf("Einladung: %v", err)
	}
	if _, err := s.CreateInvite(ctx, store.Scope{UserID: id}, "member"); err == nil {
		t.Fatal("Nicht-Admin darf einladen")
	}
	if _, err := s.AcceptInvite(ctx, inv, "neu", "passwort-neu"); err != nil {
		t.Fatal(err)
	}
	if s.InviteValid(ctx, inv) {
		t.Fatal("Einladung nach Nutzung noch gültig")
	}
	if err := s.SetDisabled(ctx, store.Scope{UserID: id, Admin: true}, id, true); err == nil {
		t.Fatal("Admin kann sich selbst sperren")
	}
}
