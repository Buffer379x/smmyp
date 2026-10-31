package prompts

import (
	"context"
	"strings"

	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/store"
)

// Sortierungen für Übersichten.
const (
	SortAuto     = ""         // Relevanz bei Textsuche, sonst zuletzt benutzt
	SortUsed     = "used"     // zuletzt benutzt
	SortCount    = "count"    // meistbenutzt
	SortTitle    = "title"    // A–Z
	SortCreated  = "created"  // neueste
	SortProvider = "provider" // nach Hauptanbieter
)

type ListOptions struct {
	Query     search.Query
	ContextID int64 // Schublade: nur Prompts dieses Anbieters, Sortierung nach dessen Nutzung
	Trash     bool
	Sort      string
	Limit     int
	Offset    int
}

type Facet struct {
	Provider providers.Provider
	Count    int
}

type Result struct {
	Items      []Prompt
	Total      int     // Treffer mit allen Filtern
	FacetTotal int     // eindeutige Treffer ohne Anbieter-Filter („Alle")
	Facets     []Facet // je Anbieter, ohne Anbieter-Filter
	Unknown    []string
}

// where sammelt Bedingungen und Argumente.
type where struct {
	join  string
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) sql() string { return w.join + " WHERE " + strings.Join(w.conds, " AND ") }

// List liefert eine Seite von Prompts samt Gesamtzahl und Facetten.
func (s *Service) List(ctx context.Context, sc store.Scope, o ListOptions) (Result, error) {
	var res Result
	all, err := providers.List(ctx, s.DB, sc)
	if err != nil {
		return res, err
	}
	if o.Limit <= 0 || o.Limit > 500 {
		o.Limit = 50
	}
	q := o.Query

	// Anbieter-Filter (@slug, kat:) auf sichtbare IDs auflösen.
	var provFilter []any
	filtered := len(q.Providers)+len(q.Categories) > 0
	for _, slug := range q.Providers {
		if p, ok := providers.BySlug(all, slug); ok {
			provFilter = append(provFilter, p.ID)
		} else {
			res.Unknown = append(res.Unknown, "@"+slug)
		}
	}
	for _, cat := range q.Categories {
		for _, p := range all {
			if p.Category == cat {
				provFilter = append(provFilter, p.ID)
			}
		}
	}

	build := func(withProviderFilter bool) *where {
		w := &where{}
		match := q.MatchExpr()
		if match != "" {
			w.join = " JOIN prompt_fts ON prompt_fts.rowid = p.id"
			w.add("prompt_fts MATCH ?", match)
		}
		w.add("p.owner_id = ?", sc.UserID)
		if o.Trash {
			w.add("p.deleted_at IS NOT NULL")
		} else {
			w.add("p.deleted_at IS NULL")
		}
		if ex := q.ExcludeExpr(); ex != "" {
			w.add("p.id NOT IN (SELECT rowid FROM prompt_fts WHERE prompt_fts MATCH ?)", ex)
		}
		for _, t := range q.Tags {
			w.add("EXISTS (SELECT 1 FROM prompt_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.prompt_id = p.id AND t.slug = ?)", t)
		}
		if q.Favorites {
			w.add("p.is_favorite = 1")
		}
		if q.UsedDays > 0 {
			w.add("p.last_used_at >= ?", store.Now().Unix()-int64(q.UsedDays)*86400)
		}
		if withProviderFilter {
			if filtered {
				w.add("EXISTS (SELECT 1 FROM prompt_providers f WHERE f.prompt_id = p.id AND f.provider_id IN ("+store.In(len(provFilter))+"))", provFilter...)
			}
			if o.ContextID > 0 {
				w.add("EXISTS (SELECT 1 FROM prompt_providers c WHERE c.prompt_id = p.id AND c.provider_id = ?)", o.ContextID)
			}
		}
		return w
	}

	w := build(true)
	if err := s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM prompts p"+w.sql(), w.args...).Scan(&res.Total); err != nil {
		return res, err
	}

	order := "ifnull(p.last_used_at, 0) DESC, p.updated_at DESC"
	args := append([]any{}, w.args...)
	switch o.Sort {
	case SortAuto:
		if q.HasText() {
			order = "bm25(prompt_fts, 8.0, 1.0, 4.0, 0.5), ifnull(p.last_used_at, 0) DESC"
		} else if o.ContextID > 0 {
			order = "ifnull((SELECT last_used_at FROM prompt_providers c2 WHERE c2.prompt_id = p.id AND c2.provider_id = ?), 0) DESC, p.updated_at DESC"
			args = append(args, o.ContextID)
		}
	case SortCount:
		order = "p.use_count DESC, ifnull(p.last_used_at, 0) DESC"
	case SortTitle:
		order = "p.title COLLATE NOCASE ASC"
	case SortCreated:
		order = "p.created_at DESC"
	case SortProvider:
		order = "(SELECT pv.name FROM prompt_providers m JOIN providers pv ON pv.id = m.provider_id WHERE m.prompt_id = p.id AND m.position = 0) COLLATE NOCASE, p.title COLLATE NOCASE"
	}
	if o.Trash {
		order = "p.deleted_at DESC"
	}
	args = append(args, o.Limit, o.Offset)
	rows, err := s.DB.R.QueryContext(ctx, "SELECT "+promptCols+" FROM prompts p"+w.sql()+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			rows.Close()
			return res, err
		}
		res.Items = append(res.Items, p)
	}
	rows.Close()
	if err := s.attach(ctx, res.Items); err != nil {
		return res, err
	}

	// Facetten: gleiche Suche ohne Anbieter-Filter; ein Prompt zählt bei jedem seiner Anbieter.
	fw := build(false)
	if err := s.DB.R.QueryRowContext(ctx, "SELECT count(*) FROM prompts p"+fw.sql(), fw.args...).Scan(&res.FacetTotal); err != nil {
		return res, err
	}
	frows, err := s.DB.R.QueryContext(ctx, "SELECT pp.provider_id, count(*) FROM prompts p"+fw.join+
		" JOIN prompt_providers pp ON pp.prompt_id = p.id WHERE "+strings.Join(fw.conds, " AND ")+" GROUP BY pp.provider_id", fw.args...)
	if err != nil {
		return res, err
	}
	defer frows.Close()
	counts := map[int64]int{}
	for frows.Next() {
		var id int64
		var n int
		if err := frows.Scan(&id, &n); err != nil {
			return res, err
		}
		counts[id] = n
	}
	for _, p := range all {
		if n := counts[p.ID]; n > 0 {
			res.Facets = append(res.Facets, Facet{Provider: p, Count: n})
		}
	}
	return res, frows.Err()
}
