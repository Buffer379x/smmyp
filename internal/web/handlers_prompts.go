package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/autotag"
	"github.com/Buffer379x/smmyp/internal/collections"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
)

type FacetVM struct {
	Name, Color, URL string
	Count            int
	Active           bool
}

type SortLink struct {
	Label, URL string
	Active     bool
}

type BrowseData struct {
	Q          string
	HasQuery   bool
	Grouped    bool
	Groups     []GroupVM
	Cards      []CardVM
	Total      int
	FacetTotal int
	Facets     []FacetVM
	AllURL     string
	AllActive  bool
	Context    *providers.Provider
	Favorites  bool
	Unknown    []string
	Sort       string
	SortLinks  []SortLink
	PrevURL    string
	NextURL    string
	From, To   int
	Onboarding bool
	NewURL     string
	Collection *collections.Collection
	Fuzzy      bool
	Bulk       bool // Listenansicht mit Mehrfachauswahl
}

var sorts = []struct{ key, label string }{
	{prompts.SortAuto, "Passend"}, {prompts.SortUsed, "Zuletzt benutzt"}, {prompts.SortCount, "Meistbenutzt"},
	{prompts.SortCreated, "Neueste"}, {prompts.SortRating, "Bewertung"}, {prompts.SortTitle, "A–Z"}, {prompts.SortProvider, "Anbieter"},
}

func validSort(s string) string {
	for _, x := range sorts {
		if x.key == s {
			return s
		}
	}
	return prompts.SortAuto
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.browseWith(w, r, u, nil)
}

// browseWith zeigt Übersicht, Schublade, Suche oder (col != nil) eine Sammlung.
func (s *Server) browseWith(w http.ResponseWriter, r *http.Request, u *auth.User, col *collections.Collection) {
	ctx := r.Context()
	p := s.page(w, r, "Prompts")
	qs := r.URL.Query()
	p.Q = strings.TrimSpace(qs.Get("q"))
	p.Scope = qs.Get("a")
	if qs.Has("partial") {
		clean := r.URL.Query()
		clean.Del("partial")
		p.Path = "/"
		if len(clean) > 0 {
			p.Path += "?" + clean.Encode()
		}
	}
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	query := search.Parse(p.Q)
	var ctxProv *providers.Provider
	switch {
	case strings.HasPrefix(p.Scope, "k:"):
		query.Categories = append(query.Categories, strings.TrimPrefix(p.Scope, "k:"))
	case p.Scope != "":
		if pr, ok := providers.BySlug(p.Rail.All, p.Scope); ok {
			ctxProv = &pr
			p.Rail.Active = pr.Slug
			p.Title = pr.Name
		} else {
			query.Providers = append(query.Providers, p.Scope)
		}
	}
	fav := qs.Get("fav") == "1"
	if fav {
		query.Favorites = true
		p.Rail.Active = "fav"
		p.Title = "Favoriten"
	}
	sortKey := validSort(qs.Get("sort"))
	offset, _ := strconv.Atoi(qs.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	grouped := p.View == "cards" && ctxProv == nil && sortKey == prompts.SortAuto && col == nil
	opts := prompts.ListOptions{Query: query, Sort: sortKey, Limit: 50, Offset: offset}
	if col != nil {
		opts.CollectionID = col.ID
		p.Title = col.Name
		p.Rail.Active = "c" + strconv.FormatInt(col.ID, 10)
	}
	if ctxProv != nil {
		opts.ContextID = ctxProv.ID
	}
	if grouped {
		opts.Limit, opts.Offset = 300, 0
	}
	res, err := s.prompts.List(ctx, u.Scope(), opts)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	link := func(extra url.Values) string {
		v := url.Values{}
		if p.Q != "" {
			v.Set("q", p.Q)
		}
		if fav {
			v.Set("fav", "1")
		}
		for k, vals := range extra {
			for _, x := range vals {
				if x != "" {
					v.Set(k, x)
				}
			}
		}
		base := "/"
		if col != nil {
			base = "/c/" + strconv.FormatInt(col.ID, 10)
		}
		if len(v) == 0 {
			return base
		}
		return base + "?" + v.Encode()
	}
	d := &BrowseData{Q: p.Q, HasQuery: !query.IsZero() || fav, Grouped: grouped, Total: res.Total, FacetTotal: res.FacetTotal,
		Context: ctxProv, Favorites: fav, Unknown: res.Unknown, Sort: sortKey, AllURL: link(nil), AllActive: ctxProv == nil,
		Onboarding: p.Rail.AllCount == 0 && p.Rail.Trash == 0 && col == nil, NewURL: "/p/new", Collection: col, Fuzzy: res.Fuzzy,
		Bulk: p.View == "list"}
	if res.Fuzzy {
		query.Fuzzy = true
	}
	if ctxProv != nil {
		d.NewURL = "/p/new?a=" + url.QueryEscape(ctxProv.Slug)
	}
	facetCount := map[int64]int{}
	for _, f := range res.Facets {
		facetCount[f.Provider.ID] = f.Count
		d.Facets = append(d.Facets, FacetVM{Name: f.Provider.Name, Color: f.Provider.Color, Count: f.Count,
			URL: link(url.Values{"a": {f.Provider.Slug}}), Active: ctxProv != nil && ctxProv.ID == f.Provider.ID})
	}
	for _, so := range sorts {
		if so.key == prompts.SortAuto && !query.HasText() && ctxProv == nil && p.View == "list" {
			continue
		}
		d.SortLinks = append(d.SortLinks, SortLink{Label: so.label, Active: so.key == sortKey, URL: link(url.Values{"a": {p.Scope}, "sort": {so.key}})})
	}
	if grouped {
		var only map[int64]bool
		if len(query.Providers)+len(query.Categories) > 0 {
			only = map[int64]bool{}
			for _, pr := range p.Rail.All {
				for _, slug := range query.Providers {
					if pr.Slug == slug {
						only[pr.ID] = true
					}
				}
				for _, c := range query.Categories {
					if pr.Category == c {
						only[pr.ID] = true
					}
				}
			}
		}
		per := 6
		if query.HasText() {
			per = 12
		}
		d.Groups = group(res.Items, p.Rail.All, only, facetCount, per, query, p.Q)
	} else {
		for _, it := range res.Items {
			d.Cards = append(d.Cards, card(it, ctxProv, query))
		}
		d.From, d.To = offset+1, offset+len(res.Items)
		base := url.Values{"a": {p.Scope}, "sort": {qs.Get("sort")}}
		if offset > 0 {
			prev := url.Values{"offset": {strconv.Itoa(max(0, offset-opts.Limit))}}
			for k, v := range base {
				prev[k] = v
			}
			d.PrevURL = link(prev)
		}
		if offset+len(res.Items) < res.Total {
			next := url.Values{"offset": {strconv.Itoa(offset + opts.Limit)}}
			for k, v := range base {
				next[k] = v
			}
			d.NextURL = link(next)
		}
	}
	p.Data = d
	if qs.Get("partial") == "1" {
		s.renderTemplate(w, http.StatusOK, "browse", "results", p)
		return
	}
	s.render(w, http.StatusOK, "browse", p)
}

// ---------- Formular ----------

type FormProv struct {
	ID               int64
	Name, Color      string
	FieldKeys        string
	Checked, Primary bool
}

type FormCat struct {
	Label     string
	Providers []FormProv
}

type FormField struct {
	Key, Label, Value string
	Long, Visible     bool
}

type FormData struct {
	ID         int64
	Action     string
	Title      string
	Model      string
	Tags       string
	Notes      string
	Favorite   bool
	Rating     int
	Links      string
	VarsOff    bool
	SourceURL  string
	Secrets    []string
	NearID     int64
	NearTitle  string
	Ratings    []int
	Categories []FormCat
	Fields     []FormField
	TagHints   []string
	Duplicate  int64
}

func (s *Server) formData(r *http.Request, u *auth.User, in prompts.Input, tagText string) (*FormData, error) {
	all, err := providers.List(r.Context(), s.db, u.Scope())
	if err != nil {
		return nil, err
	}
	selected := providers.Visible(all, in.ProviderIDs)
	sel := map[int64]bool{}
	for _, p := range selected {
		sel[p.ID] = true
	}
	var primary int64
	if len(selected) > 0 {
		primary = selected[0].ID
	}
	fd := &FormData{Title: in.Title, Model: in.Model, Notes: in.Notes, Favorite: in.Favorite, Tags: tagText,
		Rating: in.Rating, VarsOff: in.VarsOff, SourceURL: in.SourceURL, Links: linksText(in.Links), Ratings: []int{1, 2, 3, 4, 5}}
	var cur *FormCat
	for _, p := range all {
		label := providers.CategoryLabel(p.Category)
		if cur == nil || cur.Label != label {
			fd.Categories = append(fd.Categories, FormCat{Label: label})
			cur = &fd.Categories[len(fd.Categories)-1]
		}
		cur.Providers = append(cur.Providers, FormProv{ID: p.ID, Name: p.Name, Color: p.Color, FieldKeys: strings.Join(p.FieldKeys(), ","), Checked: sel[p.ID], Primary: p.ID == primary})
	}
	union := map[string]providers.Field{}
	for _, f := range providers.Union(selected) {
		union[f.Key] = f
	}
	for _, f := range providers.Catalog {
		label := f.Label
		if uf, ok := union[f.Key]; ok {
			label = uf.Label
		}
		_, inUnion := union[f.Key]
		fd.Fields = append(fd.Fields, FormField{Key: f.Key, Label: label, Long: f.Long, Value: in.Fields[f.Key], Visible: inUnion || len(selected) == 0})
	}
	top, _ := tags.Top(r.Context(), s.db.R, u.ID, 50)
	for _, t := range top {
		fd.TagHints = append(fd.TagHints, t.Slug)
	}
	return fd, nil
}

func parsePromptForm(r *http.Request) (prompts.Input, string) {
	r.ParseForm()
	in := prompts.Input{Title: r.PostForm.Get("title"), Model: r.PostForm.Get("model"), Notes: r.PostForm.Get("notes"),
		Favorite: r.PostForm.Get("favorite") == "1", Fields: map[string]string{}}
	primary, _ := strconv.ParseInt(r.PostForm.Get("primary"), 10, 64)
	var ids []int64
	for _, v := range r.PostForm["provider"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	// Hauptanbieter nach vorn (nur wenn er auch angehakt ist).
	for i, id := range ids {
		if id == primary {
			ids[0], ids[i] = ids[i], ids[0]
			break
		}
	}
	in.ProviderIDs = ids
	for _, f := range providers.Catalog {
		if v := r.PostForm.Get("field_" + f.Key); v != "" {
			in.Fields[f.Key] = v
		}
	}
	tagText := r.PostForm.Get("tags")
	in.Tags = tags.Parse(tagText)
	in.Rating, _ = strconv.Atoi(r.PostForm.Get("rating"))
	in.VarsOff = r.PostForm.Get("vars_off") == "1"
	in.SourceURL = r.PostForm.Get("source_url")
	in.Links = parseLinks(r.PostForm.Get("links"))
	return in, tagText
}

// parseLinks: eine Zeile je Link, optional „Titel | URL".
func parseLinks(text string) []prompts.Link {
	var out []prompts.Link
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		l := prompts.Link{URL: line}
		if label, u, ok := strings.Cut(line, "|"); ok {
			l = prompts.Link{Label: strings.TrimSpace(label), URL: strings.TrimSpace(u)}
		}
		out = append(out, l)
	}
	return out
}

func linksText(links []prompts.Link) string {
	var lines []string
	for _, l := range links {
		if l.Label != "" {
			lines = append(lines, l.Label+" | "+l.URL)
		} else {
			lines = append(lines, l.URL)
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Server) promptNew(w http.ResponseWriter, r *http.Request, u *auth.User) {
	qs := r.URL.Query()
	in := prompts.Input{Fields: map[string]string{}, Title: qs.Get("title"), SourceURL: qs.Get("source")}
	all, _ := providers.List(r.Context(), s.db, u.Scope())
	if slug := qs.Get("a"); slug != "" {
		if p, ok := providers.BySlug(all, slug); ok {
			in.ProviderIDs = []int64{p.ID}
		}
	}
	// Vorbelegung aus Bookmarklet oder „Teilen →": Anbieter erraten, Text ins Hauptfeld.
	if text := strings.TrimSpace(qs.Get("text")); text != "" {
		if len(in.ProviderIDs) == 0 {
			if sug, _ := s.autotag.SuggestProviders(r.Context(), u.Scope(), text, in.SourceURL, nil, s.mlOn(u)); len(sug) > 0 {
				in.ProviderIDs = []int64{sug[0].ID}
			}
		}
		key := "prompt"
		if sel := providers.Visible(all, in.ProviderIDs); len(sel) > 0 {
			key = sel[0].PrimaryField
		}
		in.Fields[key] = text
		if in.Title == "" {
			title := []rune(strings.Join(strings.Fields(text), " "))
			if len(title) > 60 {
				title = append(title[:60], '…')
			}
			in.Title = string(title)
		}
	}
	if len([]rune(in.Title)) > prompts.MaxTitleLen {
		in.Title = string([]rune(in.Title)[:prompts.MaxTitleLen])
	}
	s.renderForm(w, r, u, http.StatusOK, 0, in, "", nil, 0)
}

func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, u *auth.User, status int, id int64, in prompts.Input, tagText string, errs map[string]string, dup int64, warn ...func(*FormData)) {
	fd, err := s.formData(r, u, in, tagText)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	fd.ID, fd.Duplicate = id, dup
	for _, f := range warn {
		f(fd)
	}
	fd.Action = "/p"
	title := "Neuer Prompt"
	if id > 0 {
		fd.Action = "/p/" + strconv.FormatInt(id, 10)
		title = "Prompt bearbeiten"
	}
	p := s.page(w, r, title)
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Errors = errs
	p.Data = fd
	s.render(w, status, "form", p)
}

// warnings prüft vor dem Speichern auf Geheimnisse und (beim Anlegen) auf Duplikate.
// Liefert true, wenn das Formular mit Hinweis erneut gezeigt wurde.
func (s *Server) warnings(w http.ResponseWriter, r *http.Request, u *auth.User, id int64, in prompts.Input, tagText string) bool {
	ctx := r.Context()
	var secrets []string
	if r.PostForm.Get("secret_ok") != "1" {
		secrets = prompts.Secrets(in.Title + "\n" + in.Notes + "\n" + strings.Join(mapValues(in.Fields), "\n"))
	}
	var dup, nearID int64
	var nearTitle string
	if id == 0 && r.PostForm.Get("dup_ok") != "1" {
		dup, _ = s.prompts.Duplicate(ctx, u.Scope(), in.Title, in.Fields, 0)
		if dup == 0 {
			nearID, nearTitle, _ = s.prompts.NearDuplicate(ctx, u.Scope(), in.Title, in.Fields, 0)
		}
	}
	if len(secrets) == 0 && dup == 0 && nearID == 0 {
		return false
	}
	s.renderForm(w, r, u, http.StatusConflict, id, in, tagText, nil, dup, func(fd *FormData) {
		fd.Secrets, fd.NearID, fd.NearTitle = secrets, nearID, nearTitle
	})
	return true
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// learn speichert, welche angezeigten Vorschläge übernommen oder abgelehnt wurden (Lernsignal).
func (s *Server) learn(r *http.Request, u *auth.User, id int64, finalTags []string) {
	if !s.mlOn(u) {
		return
	}
	final := map[string]bool{}
	for _, t := range finalTags {
		final[t] = true
	}
	for _, t := range tags.Parse(r.PostForm.Get("suggested")) {
		s.autotag.RecordFeedback(r.Context(), u.Scope(), id, "tag", t, final[t])
	}
	for _, t := range tags.Parse(r.PostForm.Get("rejected")) {
		if !final[t] {
			s.autotag.RecordFeedback(r.Context(), u.Scope(), id, "tag", t, false)
		}
	}
}

// autoTags: Tags aus Regeln mit „automatisch übernehmen" – nur bei eingeschalteter KI-Hilfe.
func (s *Server) autoTags(r *http.Request, u *auth.User, in prompts.Input) []string {
	if !s.mlOn(u) {
		return nil
	}
	return s.autotag.AutoApply(r.Context(), u.Scope(), autotag.Input{Title: in.Title, Fields: in.Fields, ProviderIDs: in.ProviderIDs})
}

func (s *Server) promptCreate(w http.ResponseWriter, r *http.Request, u *auth.User) {
	in, tagText := parsePromptForm(r)
	if s.warnings(w, r, u, 0, in, tagText) {
		return
	}
	in.Tags = append(in.Tags, s.autoTags(r, u, in)...)
	id, err := s.prompts.Create(r.Context(), u.Scope(), in)
	var verr prompts.ValidationError
	if errors.As(err, &verr) {
		s.renderForm(w, r, u, http.StatusUnprocessableEntity, 0, in, tagText, verr, 0)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p, err := s.prompts.Get(r.Context(), u.Scope(), id); err == nil {
		s.learn(r, u, id, p.Tags)
	}
	s.flash(w, "ok", "Gespeichert.")
	s.redirect(w, r, "/p/"+strconv.FormatInt(id, 10))
}

func (s *Server) loadPrompt(w http.ResponseWriter, r *http.Request, u *auth.User) (prompts.Prompt, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return prompts.Prompt{}, false
	}
	pr, err := s.prompts.Get(r.Context(), u.Scope(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return pr, false
	} else if err != nil {
		s.serverError(w, r, err)
		return pr, false
	}
	return pr, true
}

func (s *Server) promptEdit(w http.ResponseWriter, r *http.Request, u *auth.User) {
	pr, ok := s.loadPrompt(w, r, u)
	if !ok {
		return
	}
	if pr.DeletedAt != 0 {
		s.notFound(w, r)
		return
	}
	s.renderForm(w, r, u, http.StatusOK, pr.ID, pr.Input(), strings.Join(pr.Tags, ", "), nil, 0)
}

func (s *Server) promptUpdate(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	in, tagText := parsePromptForm(r)
	if _, err := s.prompts.Get(r.Context(), u.Scope(), id); err != nil {
		s.notFound(w, r)
		return
	}
	if s.warnings(w, r, u, id, in, tagText) {
		return
	}
	in.Tags = append(in.Tags, s.autoTags(r, u, in)...)
	err := s.prompts.Update(r.Context(), u.Scope(), id, in)
	var verr prompts.ValidationError
	switch {
	case errors.As(err, &verr):
		s.renderForm(w, r, u, http.StatusUnprocessableEntity, id, in, tagText, verr, 0)
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
	case err != nil:
		s.serverError(w, r, err)
	default:
		if p, err := s.prompts.Get(r.Context(), u.Scope(), id); err == nil {
			s.learn(r, u, id, p.Tags)
		}
		s.flash(w, "ok", "Gespeichert.")
		s.redirect(w, r, "/p/"+strconv.FormatInt(id, 10))
	}
}

type UsageVM struct {
	Name, Color string
	UseCount    int
	LastUsed    int64
}

type DetailData struct {
	Card        CardVM
	Prompt      prompts.Prompt
	Usage       []UsageVM
	BackURL     string
	Suggestions []autotag.Suggestion
	Similar     []CardVM
	Collections []collections.Collection
	Versions    int
	Language    string
}

func (s *Server) promptShow(w http.ResponseWriter, r *http.Request, u *auth.User) {
	pr, ok := s.loadPrompt(w, r, u)
	if !ok {
		return
	}
	c := card(pr, nil, search.Query{})
	if slug := r.URL.Query().Get("a"); slug != "" {
		for _, l := range pr.Providers {
			if l.Slug == slug {
				c.ContextSlug = l.Slug
				found := false
				for i := range c.Fields {
					c.Fields[i].Primary = c.Fields[i].Key == l.PrimaryField
					found = found || c.Fields[i].Primary
				}
				if !found && len(c.Fields) > 0 {
					c.Fields[0].Primary = true
				}
			}
		}
	}
	d := &DetailData{Card: c, Prompt: pr, BackURL: "/"}
	if c.ContextSlug != "" {
		d.BackURL = "/?a=" + url.QueryEscape(c.ContextSlug)
	}
	for _, l := range pr.Providers {
		d.Usage = append(d.Usage, UsageVM{Name: l.Name, Color: l.Color, UseCount: l.UseCount, LastUsed: l.LastUsedAt})
	}
	ctx := r.Context()
	if pr.DeletedAt == 0 {
		if s.mlOn(u) {
			in := autotag.Input{PromptID: pr.ID, Title: pr.Title, Fields: pr.Fields, ProviderIDs: pr.Input().ProviderIDs, Tags: pr.Tags}
			d.Suggestions, _ = s.autotag.SuggestTags(ctx, u.Scope(), in)
		}
		if sim, err := s.prompts.Similar(ctx, u.Scope(), pr.ID, 4); err == nil {
			for _, sp := range sim {
				d.Similar = append(d.Similar, card(sp, nil, search.Query{}))
			}
		}
		d.Collections, _ = collections.ForPrompt(ctx, s.db, u.Scope(), pr.ID)
	}
	if vs, err := s.prompts.Versions(ctx, u.Scope(), pr.ID); err == nil {
		d.Versions = len(vs)
	}
	d.Language = map[string]string{"de": "Deutsch", "en": "Englisch"}[pr.Language]
	p := s.page(w, r, pr.Title)
	if err := s.shell(r, p); err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = d
	s.render(w, http.StatusOK, "detail", p)
}

func (s *Server) promptAction(w http.ResponseWriter, r *http.Request, u *auth.User, fn func(int64) error, msg, fallback string) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := fn(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.flash(w, "ok", msg)
	r.ParseForm()
	s.redirect(w, r, localPath(r.PostForm.Get("back"), fallback))
}

func (s *Server) promptTrash(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.promptAction(w, r, u, func(id int64) error { return s.prompts.Trash(r.Context(), u.Scope(), id) }, "In den Papierkorb gelegt (30 Tage wiederherstellbar).", "/")
}

func (s *Server) promptRestore(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.promptAction(w, r, u, func(id int64) error { return s.prompts.Restore(r.Context(), u.Scope(), id) }, "Wiederhergestellt.", "/trash")
}

func (s *Server) promptPurge(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.promptAction(w, r, u, func(id int64) error { return s.prompts.Purge(r.Context(), u.Scope(), id) }, "Endgültig gelöscht.", "/trash")
}

func wantsJSON(r *http.Request) bool { return r.Header.Get("X-SMMYP-Fetch") == "1" }

func (s *Server) promptFav(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	fav, err := s.prompts.ToggleFavorite(r.Context(), u.Scope(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"favorite": fav})
		return
	}
	r.ParseForm()
	s.redirect(w, r, localPath(r.PostForm.Get("back"), "/p/"+strconv.FormatInt(id, 10)))
}

func (s *Server) promptUsed(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	var providerID int64
	if slug := r.URL.Query().Get("provider"); slug != "" {
		all, _ := providers.List(r.Context(), s.db, u.Scope())
		if p, ok := providers.BySlug(all, slug); ok {
			providerID = p.ID
		}
	}
	err := s.prompts.MarkUsed(r.Context(), u.Scope(), id, providerID)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
