// Package prompts verwaltet Prompts: Anlegen, Ändern, Anbieter-Zuordnung (1..n), Papierkorb,
// Nutzung, Suche und den Volltextindex (in derselben Transaktion wie die Daten).
package prompts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

const (
	MaxFieldLen = 50_000
	MaxTitleLen = 200
	TrashDays   = 30
)

// Linked ist ein zugeordneter Anbieter inklusive Nutzung für genau diesen Anbieter.
type Linked struct {
	providers.Provider
	UseCount   int
	LastUsedAt int64
}

type Prompt struct {
	ID         int64
	OwnerID    int64
	Title      string
	Fields     map[string]string
	Notes      string
	Model      string
	IsFavorite bool
	UseCount   int
	LastUsedAt int64
	CreatedAt  int64
	UpdatedAt  int64
	DeletedAt  int64
	Providers  []Linked // [0] = Hauptanbieter
	Tags       []string
}

// Primary liefert den Hauptanbieter.
func (p Prompt) Primary() Linked {
	if len(p.Providers) == 0 {
		return Linked{}
	}
	return p.Providers[0]
}

// Provider liefert den zugeordneten Anbieter mit dieser ID (ok=false, wenn nicht zugeordnet).
func (p Prompt) Provider(id int64) (Linked, bool) {
	for _, l := range p.Providers {
		if l.ID == id {
			return l, true
		}
	}
	return Linked{}, false
}

// ProviderList liefert die Anbieter ohne Nutzungsdaten.
func (p Prompt) ProviderList() []providers.Provider {
	out := make([]providers.Provider, len(p.Providers))
	for i, l := range p.Providers {
		out[i] = l.Provider
	}
	return out
}

// Input zum Anlegen und Ändern. ProviderIDs[0] ist der Hauptanbieter.
type Input struct {
	Title       string
	ProviderIDs []int64
	Fields      map[string]string
	Notes       string
	Model       string
	Tags        []string
	Favorite    bool
}

// ValidationError: Feld → Meldung, für die Anzeige am Formularfeld.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	var msgs []string
	for k, m := range v {
		msgs = append(msgs, k+": "+m)
	}
	sort.Strings(msgs)
	return strings.Join(msgs, "; ")
}

type Service struct {
	DB *store.DB
}

// validate prüft die Eingaben und bereinigt sie (nur Felder der zugeordneten Anbieter bleiben).
func (s *Service) validate(ctx context.Context, sc store.Scope, in *Input) ([]providers.Provider, error) {
	verr := ValidationError{}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		verr["title"] = "Bitte einen Titel angeben."
	} else if len([]rune(in.Title)) > MaxTitleLen {
		verr["title"] = fmt.Sprintf("Höchstens %d Zeichen.", MaxTitleLen)
	}
	all, err := providers.List(ctx, s.DB, sc)
	if err != nil {
		return nil, err
	}
	provs := providers.Visible(all, in.ProviderIDs)
	if len(provs) == 0 {
		verr["providers"] = "Mindestens einen Anbieter wählen."
	}
	allowed := map[string]bool{}
	for _, f := range providers.Union(provs) {
		allowed[f.Key] = true
	}
	clean := map[string]string{}
	for k, v := range in.Fields {
		v = strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
		if !allowed[k] || v == "" {
			continue
		}
		if len([]rune(v)) > MaxFieldLen {
			verr["field_"+k] = fmt.Sprintf("Höchstens %d Zeichen.", MaxFieldLen)
		}
		clean[k] = v
	}
	if len(clean) == 0 && len(provs) > 0 {
		verr["fields"] = "Mindestens ein Feld ausfüllen."
	}
	in.Fields = clean
	in.Notes = strings.TrimSpace(strings.ReplaceAll(in.Notes, "\r\n", "\n"))
	if len([]rune(in.Notes)) > MaxFieldLen {
		verr["notes"] = fmt.Sprintf("Höchstens %d Zeichen.", MaxFieldLen)
	}
	in.Model = strings.TrimSpace(in.Model)
	if len([]rune(in.Model)) > 60 {
		verr["model"] = "Höchstens 60 Zeichen."
	}
	var tg []string
	seen := map[string]bool{}
	for _, t := range in.Tags {
		if t = tags.Normalize(t); t != "" && !seen[t] {
			seen[t] = true
			tg = append(tg, t)
		}
	}
	if len(tg) > tags.MaxPerPrompt {
		verr["tags"] = fmt.Sprintf("Höchstens %d Tags.", tags.MaxPerPrompt)
	}
	in.Tags = tg
	if len(verr) > 0 {
		return nil, verr
	}
	return provs, nil
}

func contentHash(title string, fields map[string]string) []byte {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.Join(strings.Fields(title), " "))))
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s\x00%s", k, strings.ToLower(strings.Join(strings.Fields(fields[k]), " ")))
	}
	return h.Sum(nil)
}

// Duplicate sucht einen inhaltsgleichen, nicht gelöschten Prompt (für den Hinweis beim Speichern).
func (s *Service) Duplicate(ctx context.Context, sc store.Scope, title string, fields map[string]string, exceptID int64) (int64, error) {
	var id int64
	err := s.DB.R.QueryRowContext(ctx, `SELECT id FROM prompts WHERE owner_id = ? AND content_hash = ? AND id <> ? AND deleted_at IS NULL LIMIT 1`,
		sc.UserID, contentHash(title, fields), exceptID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Service) Create(ctx context.Context, sc store.Scope, in Input) (int64, error) {
	provs, err := s.validate(ctx, sc, &in)
	if err != nil {
		return 0, err
	}
	now := store.Now().Unix()
	fj, _ := json.Marshal(in.Fields)
	var id int64
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO prompts(owner_id, title, fields, notes, model, is_favorite, content_hash, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, sc.UserID, in.Title, string(fj), in.Notes, in.Model, in.Favorite, contentHash(in.Title, in.Fields), now, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := setProviders(ctx, tx, sc.UserID, id, provs); err != nil {
			return err
		}
		if err := tags.Set(ctx, tx, sc.UserID, id, in.Tags); err != nil {
			return err
		}
		return reindex(ctx, tx, id)
	})
	return id, err
}

func (s *Service) Update(ctx context.Context, sc store.Scope, id int64, in Input) error {
	provs, err := s.validate(ctx, sc, &in)
	if err != nil {
		return err
	}
	fj, _ := json.Marshal(in.Fields)
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE prompts SET title=?, fields=?, notes=?, model=?, is_favorite=?, content_hash=?, updated_at=?
			WHERE id=? AND owner_id=? AND deleted_at IS NULL`,
			in.Title, string(fj), in.Notes, in.Model, in.Favorite, contentHash(in.Title, in.Fields), store.Now().Unix(), id, sc.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		if err := setProviders(ctx, tx, sc.UserID, id, provs); err != nil {
			return err
		}
		if err := tags.Set(ctx, tx, sc.UserID, id, in.Tags); err != nil {
			return err
		}
		return reindex(ctx, tx, id)
	})
}

// setProviders ersetzt die Zuordnung, behält aber die Nutzungszähler bestehender Anbieter.
func setProviders(ctx context.Context, tx *sql.Tx, owner, id int64, provs []providers.Provider) error {
	// Erst alle Positionen negativ setzen, damit der Teilindex (genau ein Hauptanbieter) nie verletzt wird.
	if _, err := tx.ExecContext(ctx, "UPDATE prompt_providers SET position = -1 - position WHERE prompt_id = ?", id); err != nil {
		return err
	}
	ids := make([]any, 0, len(provs)+1)
	ids = append(ids, id)
	for _, p := range provs {
		ids = append(ids, p.ID)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_providers WHERE prompt_id = ? AND provider_id NOT IN ("+store.In(len(provs))+")", ids...); err != nil {
		return err
	}
	for pos, p := range provs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_providers(prompt_id, provider_id, owner_id, position) VALUES (?,?,?,?)
			ON CONFLICT(prompt_id, provider_id) DO UPDATE SET position = excluded.position`, id, p.ID, owner, pos); err != nil {
			return err
		}
	}
	return nil
}

// reindex schreibt den Volltexteintrag eines Prompts neu.
func reindex(ctx context.Context, tx *sql.Tx, id int64) error {
	var title, fieldsJSON, notes, model string
	if err := tx.QueryRowContext(ctx, "SELECT title, fields, notes, model FROM prompts WHERE id = ?", id).Scan(&title, &fieldsJSON, &notes, &model); err != nil {
		return err
	}
	var fields map[string]string
	json.Unmarshal([]byte(fieldsJSON), &fields)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, k := range keys {
		body.WriteString(fields[k])
		body.WriteString("\n")
	}
	body.WriteString(model)
	var tagText string
	tx.QueryRowContext(ctx, `SELECT ifnull(group_concat(t.slug, ' '), '') FROM prompt_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.prompt_id = ?`, id).Scan(&tagText)
	if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_fts WHERE rowid = ?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO prompt_fts(rowid, title, body, tags, notes) VALUES (?,?,?,?,?)", id, title, body.String(), tagText, notes)
	return err
}

// Reindex baut den gesamten Suchindex neu auf (Befehl `smmyp reindex`).
func (s *Service) Reindex(ctx context.Context) (int, error) {
	n := 0
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_fts"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM prompts")
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := reindex(ctx, tx, id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

const promptCols = "p.id, p.owner_id, p.title, p.fields, p.notes, p.model, p.is_favorite, p.use_count, ifnull(p.last_used_at,0), p.created_at, p.updated_at, ifnull(p.deleted_at,0)"

func scanPrompt(row interface{ Scan(...any) error }) (Prompt, error) {
	var p Prompt
	var fields string
	err := row.Scan(&p.ID, &p.OwnerID, &p.Title, &fields, &p.Notes, &p.Model, &p.IsFavorite, &p.UseCount, &p.LastUsedAt, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if err != nil {
		return p, err
	}
	json.Unmarshal([]byte(fields), &p.Fields)
	if p.Fields == nil {
		p.Fields = map[string]string{}
	}
	return p, nil
}

// Get liefert einen Prompt des Benutzers (auch aus dem Papierkorb). Fremde IDs → ErrNotFound.
func (s *Service) Get(ctx context.Context, sc store.Scope, id int64) (Prompt, error) {
	p, err := scanPrompt(s.DB.R.QueryRowContext(ctx, "SELECT "+promptCols+" FROM prompts p WHERE p.id = ? AND p.owner_id = ?", id, sc.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, store.ErrNotFound
	} else if err != nil {
		return p, err
	}
	list := []Prompt{p}
	if err := s.attach(ctx, list); err != nil {
		return p, err
	}
	return list[0], nil
}

// attach lädt Anbieter und Tags für eine Liste von Prompts in zwei Abfragen.
func (s *Service) attach(ctx context.Context, list []Prompt) error {
	if len(list) == 0 {
		return nil
	}
	idx := map[int64]int{}
	args := make([]any, len(list))
	for i, p := range list {
		idx[p.ID] = i
		args[i] = p.ID
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT pp.prompt_id, pp.use_count, ifnull(pp.last_used_at,0),
		pv.id, ifnull(pv.owner_id,0), pv.slug, pv.name, pv.category, pv.color, pv.fields, pv.primary_field, pv.sort
		FROM prompt_providers pp JOIN providers pv ON pv.id = pp.provider_id
		WHERE pp.prompt_id IN (`+store.In(len(args))+`) ORDER BY pp.prompt_id, pp.position`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pid int64
		var l Linked
		var fields string
		if err := rows.Scan(&pid, &l.UseCount, &l.LastUsedAt, &l.ID, &l.OwnerID, &l.Slug, &l.Name, &l.Category, &l.Color, &fields, &l.PrimaryField, &l.Sort); err != nil {
			rows.Close()
			return err
		}
		json.Unmarshal([]byte(fields), &l.Fields)
		list[idx[pid]].Providers = append(list[idx[pid]].Providers, l)
	}
	rows.Close()
	rows, err = s.DB.R.QueryContext(ctx, `SELECT pt.prompt_id, t.slug FROM prompt_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.prompt_id IN (`+store.In(len(args))+`) ORDER BY t.slug`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var slug string
		if err := rows.Scan(&pid, &slug); err != nil {
			return err
		}
		list[idx[pid]].Tags = append(list[idx[pid]].Tags, slug)
	}
	return rows.Err()
}

func (s *Service) exec(ctx context.Context, q string, args ...any) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// Trash verschiebt in den Papierkorb.
func (s *Service) Trash(ctx context.Context, sc store.Scope, id int64) error {
	return s.exec(ctx, "UPDATE prompts SET deleted_at = ? WHERE id = ? AND owner_id = ? AND deleted_at IS NULL", store.Now().Unix(), id, sc.UserID)
}

// Restore holt aus dem Papierkorb zurück.
func (s *Service) Restore(ctx context.Context, sc store.Scope, id int64) error {
	return s.exec(ctx, "UPDATE prompts SET deleted_at = NULL WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL", id, sc.UserID)
}

// Purge löscht einen Prompt aus dem Papierkorb endgültig (inkl. Suchindex).
func (s *Service) Purge(ctx context.Context, sc store.Scope, id int64) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM prompts WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL", id, sc.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_fts WHERE rowid = ?", id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE owner_id = ? AND id NOT IN (SELECT tag_id FROM prompt_tags)`, sc.UserID)
		return err
	})
}

// PurgeExpired löscht alle Papierkorb-Einträge, die älter als TrashDays sind (alle Benutzer).
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	cutoff := store.Now().Add(-TrashDays * 24 * time.Hour).Unix()
	var n int64
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM prompt_fts WHERE rowid IN (SELECT id FROM prompts WHERE deleted_at IS NOT NULL AND deleted_at < ?)", cutoff); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM prompts WHERE deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, "DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM prompt_tags)")
		return err
	})
	return n, err
}

// ToggleFavorite schaltet den Favoriten-Status um und liefert den neuen Wert.
func (s *Service) ToggleFavorite(ctx context.Context, sc store.Scope, id int64) (bool, error) {
	var fav bool
	err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE prompts SET is_favorite = 1 - is_favorite WHERE id = ? AND owner_id = ? AND deleted_at IS NULL
			RETURNING is_favorite`, id, sc.UserID).Scan(&fav)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	})
	return fav, err
}

// MarkUsed zählt eine Nutzung (gesamt und für den Anbieter; ohne gültigen Anbieter für den Hauptanbieter).
func (s *Service) MarkUsed(ctx context.Context, sc store.Scope, id, providerID int64) error {
	now := store.Now().Unix()
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE prompts SET use_count = use_count + 1, last_used_at = ? WHERE id = ? AND owner_id = ? AND deleted_at IS NULL", now, id, sc.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		res, err = tx.ExecContext(ctx, "UPDATE prompt_providers SET use_count = use_count + 1, last_used_at = ? WHERE prompt_id = ? AND provider_id = ?", now, id, providerID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_, err = tx.ExecContext(ctx, "UPDATE prompt_providers SET use_count = use_count + 1, last_used_at = ? WHERE prompt_id = ? AND position = 0", now, id)
		}
		return err
	})
}

// Counts für die Schubladen-Navigation.
type Counts struct {
	All, Favorites, Trash int
	ByProvider            map[int64]int
}

func (s *Service) Counts(ctx context.Context, sc store.Scope) (Counts, error) {
	c := Counts{ByProvider: map[int64]int{}}
	err := s.DB.R.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE deleted_at IS NULL),
		count(*) FILTER (WHERE deleted_at IS NULL AND is_favorite = 1),
		count(*) FILTER (WHERE deleted_at IS NOT NULL)
		FROM prompts WHERE owner_id = ?`, sc.UserID).Scan(&c.All, &c.Favorites, &c.Trash)
	if err != nil {
		return c, err
	}
	rows, err := s.DB.R.QueryContext(ctx, `SELECT pp.provider_id, count(*) FROM prompt_providers pp JOIN prompts p ON p.id = pp.prompt_id
		WHERE pp.owner_id = ? AND p.deleted_at IS NULL GROUP BY pp.provider_id`, sc.UserID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return c, err
		}
		c.ByProvider[id] = n
	}
	return c, rows.Err()
}

// ExportPrompt ist das Format des JSON-Exports (wieder importierbar ab Phase 2).
type ExportPrompt struct {
	Title     string            `json:"title"`
	Providers []string          `json:"providers"`
	Fields    map[string]string `json:"fields"`
	Notes     string            `json:"notes,omitempty"`
	Model     string            `json:"model,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Favorite  bool              `json:"favorite,omitempty"`
	UseCount  int               `json:"use_count"`
	Created   time.Time         `json:"created"`
	Updated   time.Time         `json:"updated"`
}

// Export liefert alle nicht gelöschten Prompts des Benutzers.
func (s *Service) Export(ctx context.Context, sc store.Scope) ([]ExportPrompt, error) {
	rows, err := s.DB.R.QueryContext(ctx, "SELECT "+promptCols+" FROM prompts p WHERE p.owner_id = ? AND p.deleted_at IS NULL ORDER BY p.id", sc.UserID)
	if err != nil {
		return nil, err
	}
	var list []Prompt
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := s.attach(ctx, list); err != nil {
		return nil, err
	}
	out := make([]ExportPrompt, 0, len(list))
	for _, p := range list {
		e := ExportPrompt{Title: p.Title, Fields: p.Fields, Notes: p.Notes, Model: p.Model, Tags: p.Tags,
			Favorite: p.IsFavorite, UseCount: p.UseCount, Created: time.Unix(p.CreatedAt, 0).UTC(), Updated: time.Unix(p.UpdatedAt, 0).UTC()}
		for _, l := range p.Providers {
			e.Providers = append(e.Providers, l.Slug)
		}
		out = append(out, e)
	}
	return out, nil
}
