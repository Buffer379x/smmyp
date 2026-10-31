package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/autotag"
	"github.com/Buffer379x/smmyp/internal/collections"
	"github.com/Buffer379x/smmyp/internal/diff"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/store"
	"github.com/Buffer379x/smmyp/internal/tags"
	"github.com/Buffer379x/smmyp/internal/vars"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleErr bildet Fehler auf Antworten ab; true = Fehler wurde behandelt.
func (s *Server) handleErr(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
	default:
		s.serverError(w, r, err)
	}
	return true
}

func errorText(err error) string {
	var pv prompts.ValidationError
	var cv collections.ValidationError
	switch {
	case errors.As(err, &pv):
		for _, m := range pv {
			return m
		}
	case errors.As(err, &cv):
		for _, m := range cv {
			return m
		}
	}
	return err.Error()
}

// ---------- Versionen ----------

type FieldDiff struct {
	Label string
	Ops   []diff.Op
}

type VersionVM struct {
	ID        int64
	CreatedAt int64
	Title     string
	Selected  bool
}

type VersionsData struct {
	Prompt   prompts.Prompt
	Versions []VersionVM
	FromID   int64
	FromDate int64
	Diffs    []FieldDiff
}

func (s *Server) promptVersions(w http.ResponseWriter, r *http.Request, u *auth.User) {
	pr, ok := s.loadPrompt(w, r, u)
	if !ok {
		return
	}
	vs, err := s.prompts.Versions(r.Context(), u.Scope(), pr.ID)
	if s.handleErr(w, r, err) {
		return
	}
	d := &VersionsData{Prompt: pr}
	sel, _ := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64)
	var from *prompts.Version
	for i := range vs {
		if (sel == 0 && i == 0) || vs[i].ID == sel {
			from = &vs[i]
		}
	}
	for _, v := range vs {
		d.Versions = append(d.Versions, VersionVM{ID: v.ID, CreatedAt: v.CreatedAt, Title: v.Title, Selected: from != nil && from.ID == v.ID})
	}
	if from != nil {
		d.FromID, d.FromDate = from.ID, from.CreatedAt
		if ops := diff.Words(from.Title, pr.Title); diff.Changed(ops) {
			d.Diffs = append(d.Diffs, FieldDiff{Label: "Titel", Ops: ops})
		}
		labels := map[string]string{}
		for _, f := range providers.Catalog {
			labels[f.Key] = f.Label
		}
		for _, f := range providers.Union(pr.ProviderList()) {
			labels[f.Key] = f.Label
		}
		for _, f := range providers.Catalog {
			if ops := diff.Words(from.Fields[f.Key], pr.Fields[f.Key]); diff.Changed(ops) {
				d.Diffs = append(d.Diffs, FieldDiff{Label: labels[f.Key], Ops: ops})
			}
		}
		if ops := diff.Words(from.Notes, pr.Notes); diff.Changed(ops) {
			d.Diffs = append(d.Diffs, FieldDiff{Label: "Notizen", Ops: ops})
		}
	}
	p := s.page(w, r, "Versionen: "+pr.Title)
	if err := s.shell(r, p); s.handleErr(w, r, err) {
		return
	}
	p.Data = d
	s.render(w, http.StatusOK, "versions", p)
}

func (s *Server) promptVersionRestore(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	vid, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if !ok || err != nil {
		s.notFound(w, r)
		return
	}
	err = s.prompts.RestoreVersion(r.Context(), u.Scope(), id, vid)
	var verr prompts.ValidationError
	if errors.As(err, &verr) {
		s.flash(w, "error", "Wiederherstellen nicht möglich: "+errorText(err))
		s.redirect(w, r, "/p/"+strconv.FormatInt(id, 10)+"/versions")
		return
	}
	if s.handleErr(w, r, err) {
		return
	}
	s.flash(w, "ok", "Version wiederhergestellt. Die vorherige Fassung liegt jetzt in den Versionen.")
	s.redirect(w, r, "/p/"+strconv.FormatInt(id, 10))
}

// ---------- Platzhalter ----------

func (s *Server) promptVarsGet(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	pr, err := s.prompts.Get(r.Context(), u.Scope(), id)
	if s.handleErr(w, r, err) {
		return
	}
	last, err := s.prompts.VarValues(r.Context(), u.Scope(), id)
	if s.handleErr(w, r, err) {
		return
	}
	type v struct {
		Name    string `json:"name"`
		Default string `json:"default"`
		Last    string `json:"last"`
	}
	out := []v{}
	if !pr.VarsOff {
		for _, x := range vars.Parse(mapValues(pr.Fields)...) {
			out = append(out, v{x.Name, x.Default, last[x.Name]})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) promptVarsSave(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	if _, err := s.prompts.Get(r.Context(), u.Scope(), id); s.handleErr(w, r, err) {
		return // erst Besitz prüfen, dann den Inhalt (fremde IDs → immer 404)
	}
	var values map[string]string
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&values); err != nil {
		http.Error(w, "ungültiges JSON", http.StatusBadRequest)
		return
	}
	if s.handleErr(w, r, s.prompts.SaveVarValues(r.Context(), u.Scope(), id, values)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Vorschläge am gespeicherten Prompt ----------

// promptTagSuggestion: Vorschlag übernehmen (action=accept) oder ablehnen (action=reject).
func (s *Server) promptTagSuggestion(w http.ResponseWriter, r *http.Request, u *auth.User) {
	pr, ok := s.loadPrompt(w, r, u)
	if !ok {
		return
	}
	tag := tags.Normalize(r.PostFormValue("tag"))
	if tag == "" || pr.DeletedAt != 0 || !s.mlOn(u) {
		s.notFound(w, r)
		return
	}
	accept := r.PostFormValue("action") == "accept"
	if accept {
		in := pr.Input()
		in.Tags = append(in.Tags, tag)
		if err := s.prompts.Update(r.Context(), u.Scope(), pr.ID, in); s.handleErr(w, r, err) {
			return
		}
	}
	if err := s.autotag.RecordFeedback(r.Context(), u.Scope(), pr.ID, "tag", tag, accept); s.handleErr(w, r, err) {
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"tag": tag, "accepted": accept})
		return
	}
	s.redirect(w, r, "/p/"+strconv.FormatInt(pr.ID, 10))
}

// promptCollections setzt die Sammlungen eines Prompts (Checkboxen in der Detailansicht) oder legt eine neue an.
func (s *Server) promptCollections(w http.ResponseWriter, r *http.Request, u *auth.User) {
	pr, ok := s.loadPrompt(w, r, u)
	if !ok {
		return
	}
	ctx := r.Context()
	r.ParseForm()
	want := map[int64]bool{}
	for _, v := range r.PostForm["collection"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[id] = true
		}
	}
	if name := strings.TrimSpace(r.PostForm.Get("new")); name != "" {
		id, err := collections.Create(ctx, s.db, u.Scope(), name, "")
		if err != nil {
			s.flash(w, "error", errorText(err))
			s.redirect(w, r, "/p/"+strconv.FormatInt(pr.ID, 10))
			return
		}
		want[id] = true
	}
	cur, err := collections.ForPrompt(ctx, s.db, u.Scope(), pr.ID)
	if s.handleErr(w, r, err) {
		return
	}
	for _, c := range cur {
		switch {
		case want[c.ID] && !c.Member:
			_, err = collections.Add(ctx, s.db, u.Scope(), c.ID, []int64{pr.ID})
		case !want[c.ID] && c.Member:
			err = collections.Remove(ctx, s.db, u.Scope(), c.ID, pr.ID)
		}
		if s.handleErr(w, r, err) {
			return
		}
		delete(want, c.ID)
	}
	for id := range want { // neu angelegte Sammlung
		if _, err := collections.Add(ctx, s.db, u.Scope(), id, []int64{pr.ID}); s.handleErr(w, r, err) {
			return
		}
	}
	s.flash(w, "ok", "Sammlungen gespeichert.")
	s.redirect(w, r, "/p/"+strconv.FormatInt(pr.ID, 10))
}

// ---------- API für das Formular und die Befehlspalette ----------

type suggestResponse struct {
	Tags      []autotag.Suggestion         `json:"tags"`
	Providers []autotag.ProviderSuggestion `json:"providers"`
	Duplicate *ref                         `json:"duplicate,omitempty"`
	Near      *ref                         `json:"near,omitempty"`
	Secrets   []string                     `json:"secrets,omitempty"`
	ML        bool                         `json:"ml"`
	Vars      []string                     `json:"vars,omitempty"`
}

type ref struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// apiSuggest wertet das Formular live aus (Vorschläge, Duplikate, Geheimnisse, Platzhalter).
func (s *Server) apiSuggest(w http.ResponseWriter, r *http.Request, u *auth.User) {
	in, _ := parsePromptForm(r)
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
	if id > 0 {
		if _, err := s.prompts.Get(ctx, u.Scope(), id); err != nil {
			id = 0 // fremde oder unbekannte ID: wie ein neuer Prompt behandeln
		}
	}
	ai := autotag.Input{PromptID: id, Title: in.Title, Fields: in.Fields, ProviderIDs: in.ProviderIDs, Tags: in.Tags, SourceURL: in.SourceURL}
	resp := suggestResponse{Tags: []autotag.Suggestion{}, Providers: []autotag.ProviderSuggestion{}, ML: s.mlOn(u)}
	var err error
	text := in.Title + "\n" + strings.Join(mapValues(in.Fields), "\n")
	if resp.ML { // Vorschläge nur mit eingeschalteter KI-Hilfe; Prüfungen unten laufen immer
		if resp.Tags, err = s.autotag.SuggestTags(ctx, u.Scope(), ai); err != nil {
			s.serverError(w, r, err)
			return
		}
		if resp.Providers, err = s.autotag.SuggestProviders(ctx, u.Scope(), text, in.SourceURL, in.ProviderIDs, true); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if strings.TrimSpace(strings.Join(mapValues(in.Fields), "")) != "" {
		if dup, _ := s.prompts.Duplicate(ctx, u.Scope(), in.Title, in.Fields, id); dup > 0 {
			if p, err := s.prompts.Get(ctx, u.Scope(), dup); err == nil {
				resp.Duplicate = &ref{dup, p.Title}
			}
		} else if near, title, _ := s.prompts.NearDuplicate(ctx, u.Scope(), in.Title, in.Fields, id); near > 0 {
			resp.Near = &ref{near, title}
		}
	}
	resp.Secrets = prompts.Secrets(text + "\n" + in.Notes)
	if !in.VarsOff {
		for _, v := range vars.Parse(mapValues(in.Fields)...) {
			resp.Vars = append(resp.Vars, v.Name)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) apiPalette(w http.ResponseWriter, r *http.Request, u *auth.User) {
	type item struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		Providers string `json:"providers"`
	}
	out := []item{}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" {
		res, err := s.prompts.List(r.Context(), u.Scope(), prompts.ListOptions{Query: search.Parse(q), Limit: 8})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, p := range res.Items {
			c := card(p, nil, search.Query{})
			out = append(out, item{Title: p.Title, URL: c.URL, Providers: c.Names})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- Mehrfachauswahl ----------

func (s *Server) bulk(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.ParseForm()
	back := localPath(r.PostForm.Get("back"), "/")
	var ids []int64
	for _, v := range r.PostForm["id"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 500 {
		s.flash(w, "error", "Bitte zuerst Prompts auswählen.")
		s.redirect(w, r, back)
		return
	}
	ctx := r.Context()
	action := r.PostForm.Get("action")
	var n int
	var err error
	switch action {
	case "collection":
		var cid int64
		if name := strings.TrimSpace(r.PostForm.Get("new_collection")); name != "" {
			cid, err = collections.Create(ctx, s.db, u.Scope(), name, "")
		} else {
			cid, err = strconv.ParseInt(r.PostForm.Get("collection"), 10, 64)
		}
		if err == nil {
			n, err = collections.Add(ctx, s.db, u.Scope(), cid, ids)
		}
	case "tag":
		n, err = s.prompts.Bulk(ctx, u.Scope(), ids, "tag", r.PostForm.Get("tag"))
	case "provider":
		n, err = s.prompts.Bulk(ctx, u.Scope(), ids, "provider", r.PostForm.Get("provider"))
	case "trash":
		n, err = s.prompts.Bulk(ctx, u.Scope(), ids, "trash", "")
	default:
		err = prompts.ValidationError{"action": "Aktion wählen."}
	}
	var pv prompts.ValidationError
	var cv collections.ValidationError
	switch {
	case errors.As(err, &pv), errors.As(err, &cv), errors.Is(err, store.ErrNotFound), err != nil && strings.Contains(err.Error(), "strconv"):
		s.flash(w, "error", "Aktion nicht möglich: "+errorText(err))
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		s.flash(w, "ok", strconv.Itoa(n)+" Prompt(s) geändert.")
	}
	s.redirect(w, r, back)
}

// ---------- Tags, Aliase, Regeln, ML-Status ----------

type TagsData struct {
	Tags       []prompts.TagInfo
	Rules      []tags.Rule
	Status     autotag.Status
	Categories []providers.Category
	MinKNN     int
	MinModel   int
	QualityMin float64
}

func (s *Server) tagsPage(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.renderTags(w, r, u, http.StatusOK, nil)
}

func (s *Server) renderTags(w http.ResponseWriter, r *http.Request, u *auth.User, status int, errs map[string]string) {
	ctx := r.Context()
	p := s.page(w, r, "Tags & KI-Hilfe")
	if err := s.shell(r, p); s.handleErr(w, r, err) {
		return
	}
	d := &TagsData{Categories: providers.Categories, MinKNN: autotag.MinKNN, MinModel: autotag.MinModel, QualityMin: autotag.QualityMin}
	var err error
	if d.Tags, err = s.prompts.TagList(ctx, u.Scope()); s.handleErr(w, r, err) {
		return
	}
	if d.Rules, err = tags.Rules(ctx, s.db.R, u.ID); s.handleErr(w, r, err) {
		return
	}
	if d.Status, err = s.autotag.Status(ctx, u.Scope()); s.handleErr(w, r, err) {
		return
	}
	r.ParseForm()
	p.Form, p.Errors, p.Data = r.PostForm, errs, d
	s.render(w, status, "tags", p)
}

func (s *Server) tagAction(w http.ResponseWriter, r *http.Request, u *auth.User, err error, ok string) {
	var pv prompts.ValidationError
	switch {
	case errors.As(err, &pv):
		s.renderTags(w, r, u, http.StatusUnprocessableEntity, pv)
		return
	case errors.Is(err, store.ErrNotFound):
		s.flash(w, "error", "Nicht gefunden.")
	case err != nil:
		s.serverError(w, r, err)
		return
	default:
		s.flash(w, "ok", ok)
	}
	s.redirect(w, r, "/tags")
}

func (s *Server) tagRename(w http.ResponseWriter, r *http.Request, u *auth.User) {
	n, err := s.prompts.RenameTag(r.Context(), u.Scope(), r.PostFormValue("from"), r.PostFormValue("to"))
	s.tagAction(w, r, u, err, "Tag geändert ("+strconv.Itoa(n)+" Prompts). Der alte Name ist jetzt ein Alias.")
}

func (s *Server) tagAliasAdd(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.tagAction(w, r, u, s.prompts.AddAlias(r.Context(), u.Scope(), r.PostFormValue("alias"), r.PostFormValue("tag")), "Alias angelegt.")
}

func (s *Server) tagAliasDelete(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.tagAction(w, r, u, s.prompts.DeleteAlias(r.Context(), u.Scope(), r.PostFormValue("alias")), "Alias gelöscht.")
}

func (s *Server) tagRuleAdd(w http.ResponseWriter, r *http.Request, u *auth.User) {
	rule := tags.Rule{Kind: r.PostFormValue("kind"), Pattern: r.PostFormValue("pattern"), Tag: r.PostFormValue("tag"),
		Category: r.PostFormValue("category"), AutoApply: r.PostFormValue("auto") == "1"}
	s.tagAction(w, r, u, s.prompts.AddRule(r.Context(), u.Scope(), rule), "Regel angelegt.")
}

func (s *Server) tagRuleDelete(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := s.prompts.DeleteRule(r.Context(), u.Scope(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.tagAction(w, r, u, err, "Regel gelöscht.")
}

func (s *Server) tagTrain(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if !s.mlOn(u) {
		s.flash(w, "error", "Die KI-Hilfe ist ausgeschaltet (Einstellungen).")
		s.redirect(w, r, "/tags")
		return
	}
	err := s.autotag.Train(r.Context(), u.ID)
	s.tagAction(w, r, u, err, "Modelle neu trainiert.")
}

// ---------- Sammlungen ----------

type CollectionsData struct {
	Collections []collections.Collection
}

func (s *Server) collectionsPage(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.renderCollections(w, r, u, http.StatusOK, nil)
}

func (s *Server) renderCollections(w http.ResponseWriter, r *http.Request, u *auth.User, status int, errs map[string]string) {
	p := s.page(w, r, "Sammlungen")
	if err := s.shell(r, p); s.handleErr(w, r, err) {
		return
	}
	p.Rail.Active = "c"
	r.ParseForm()
	p.Form, p.Errors = r.PostForm, errs
	p.Data = &CollectionsData{Collections: p.Rail.Collections}
	s.render(w, status, "collections", p)
}

func (s *Server) collectionCreate(w http.ResponseWriter, r *http.Request, u *auth.User) {
	id, err := collections.Create(r.Context(), s.db, u.Scope(), r.PostFormValue("name"), r.PostFormValue("notes"))
	var cv collections.ValidationError
	if errors.As(err, &cv) {
		s.renderCollections(w, r, u, http.StatusUnprocessableEntity, cv)
		return
	}
	if s.handleErr(w, r, err) {
		return
	}
	s.flash(w, "ok", "Sammlung angelegt. Prompts fügst du in der Detailansicht oder per Mehrfachauswahl in der Liste hinzu.")
	s.redirect(w, r, "/c/"+strconv.FormatInt(id, 10))
}

func (s *Server) loadCollection(w http.ResponseWriter, r *http.Request, u *auth.User) (collections.Collection, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return collections.Collection{}, false
	}
	c, err := collections.Get(r.Context(), s.db, u.Scope(), id)
	return c, !s.handleErr(w, r, err)
}

func (s *Server) collectionShow(w http.ResponseWriter, r *http.Request, u *auth.User) {
	if c, ok := s.loadCollection(w, r, u); ok {
		s.browseWith(w, r, u, &c)
	}
}

func (s *Server) collectionUpdate(w http.ResponseWriter, r *http.Request, u *auth.User) {
	c, ok := s.loadCollection(w, r, u)
	if !ok {
		return
	}
	err := collections.Update(r.Context(), s.db, u.Scope(), c.ID, r.PostFormValue("name"), r.PostFormValue("notes"))
	var cv collections.ValidationError
	if errors.As(err, &cv) {
		s.flash(w, "error", errorText(err))
	} else if s.handleErr(w, r, err) {
		return
	} else {
		s.flash(w, "ok", "Sammlung gespeichert.")
	}
	s.redirect(w, r, "/c/"+strconv.FormatInt(c.ID, 10))
}

func (s *Server) collectionDelete(w http.ResponseWriter, r *http.Request, u *auth.User) {
	c, ok := s.loadCollection(w, r, u)
	if !ok {
		return
	}
	if s.handleErr(w, r, collections.Delete(r.Context(), s.db, u.Scope(), c.ID)) {
		return
	}
	s.flash(w, "ok", "Sammlung gelöscht. Die Prompts bleiben erhalten.")
	s.redirect(w, r, "/c")
}

func (s *Server) collectionRemove(w http.ResponseWriter, r *http.Request, u *auth.User) {
	c, ok := s.loadCollection(w, r, u)
	if !ok {
		return
	}
	pid, _ := strconv.ParseInt(r.PostFormValue("prompt"), 10, 64)
	if s.handleErr(w, r, collections.Remove(r.Context(), s.db, u.Scope(), c.ID, pid)) {
		return
	}
	s.redirect(w, r, localPath(r.PostFormValue("back"), "/c/"+strconv.FormatInt(c.ID, 10)))
}

// ---------- Import ----------

type ImportData struct {
	Report *prompts.ImportReport
}

const maxImport = 5 << 20

func (s *Server) importPage(w http.ResponseWriter, r *http.Request, u *auth.User) {
	s.renderImport(w, r, u, http.StatusOK, nil, nil)
}

func (s *Server) renderImport(w http.ResponseWriter, r *http.Request, u *auth.User, status int, rep *prompts.ImportReport, errs map[string]string) {
	p := s.page(w, r, "Import")
	if err := s.shell(r, p); s.handleErr(w, r, err) {
		return
	}
	p.Errors, p.Data = errs, &ImportData{Report: rep}
	s.render(w, status, "import", p)
}

func (s *Server) importSubmit(w http.ResponseWriter, r *http.Request, u *auth.User) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImport+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.renderImport(w, r, u, http.StatusBadRequest, nil, map[string]string{"file": "Datei fehlt oder ist größer als 5 MB."})
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.renderImport(w, r, u, http.StatusBadRequest, nil, map[string]string{"file": "Bitte eine Datei wählen."})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImport))
	if err != nil {
		s.renderImport(w, r, u, http.StatusBadRequest, nil, map[string]string{"file": "Datei nicht lesbar."})
		return
	}
	var items []prompts.ExportPrompt
	if strings.HasSuffix(strings.ToLower(hdr.Filename), ".csv") {
		items, err = parseCSV(data)
	} else {
		var wrapped struct {
			Format  string                 `json:"format"`
			Prompts []prompts.ExportPrompt `json:"prompts"`
		}
		if err = json.Unmarshal(data, &wrapped); err == nil && wrapped.Format != "smmyp-export" {
			err = errors.New("kein SMMYP-Export (Feld \"format\" fehlt)")
		}
		items = wrapped.Prompts
	}
	if err != nil {
		s.renderImport(w, r, u, http.StatusUnprocessableEntity, nil, map[string]string{"file": "Format nicht erkannt: " + err.Error()})
		return
	}
	if len(items) > 5000 {
		s.renderImport(w, r, u, http.StatusUnprocessableEntity, nil, map[string]string{"file": "Höchstens 5000 Prompts je Import."})
		return
	}
	rep, err := s.prompts.Import(r.Context(), u.Scope(), items)
	if s.handleErr(w, r, err) {
		return
	}
	s.log.Info("Import", "user_id", u.ID, "importiert", rep.Imported, "duplikate", rep.Duplicates, "fehler", len(rep.Failed))
	s.renderImport(w, r, u, http.StatusOK, &rep, nil)
}

// parseCSV: Kopfzeile mit title, providers, tags, notes, model und beliebigen Feldschlüsseln (prompt, style …).
func parseCSV(data []byte) ([]prompts.ExportPrompt, error) {
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	rd.FieldsPerRecord = -1
	if strings.Count(strings.SplitN(string(data), "\n", 2)[0], ";") > strings.Count(strings.SplitN(string(data), "\n", 2)[0], ",") {
		rd.Comma = ';' // deutsches Excel
	}
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, errors.New("CSV braucht eine Kopfzeile und mindestens eine Zeile")
	}
	head := rows[0]
	fieldKeys := map[string]bool{}
	for _, f := range providers.Catalog {
		fieldKeys[f.Key] = true
	}
	var out []prompts.ExportPrompt
	for _, row := range rows[1:] {
		p := prompts.ExportPrompt{Fields: map[string]string{}}
		for i, col := range head {
			if i >= len(row) {
				break
			}
			v := strings.TrimSpace(row[i])
			switch key := strings.ToLower(strings.TrimSpace(col)); {
			case key == "title" || key == "titel":
				p.Title = v
			case key == "providers" || key == "provider" || key == "anbieter":
				p.Providers = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' || r == ';' })
			case key == "tags":
				p.Tags = tags.Parse(v)
			case key == "notes" || key == "notizen":
				p.Notes = v
			case key == "model" || key == "modell":
				p.Model = v
			case fieldKeys[key]:
				p.Fields[key] = v
			}
		}
		if p.Title != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// ---------- PWA ----------

// share: Ziel für „Teilen →" (Android) – leitet ins Formular mit Vorbelegung.
func (s *Server) share(w http.ResponseWriter, r *http.Request, u *auth.User) {
	q := r.URL.Query()
	v := url.Values{}
	text := strings.TrimSpace(q.Get("text"))
	link := strings.TrimSpace(q.Get("url"))
	if text == "" {
		text = link
	}
	if text != "" {
		v.Set("text", text)
	}
	if t := strings.TrimSpace(q.Get("title")); t != "" {
		v.Set("title", t)
	}
	if strings.HasPrefix(link, "http") {
		v.Set("source", link)
	}
	s.redirect(w, r, "/p/new?"+v.Encode())
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{
		"name": "SMMYP – Show Me My Prompts", "short_name": "SMMYP", "lang": "de",
		"start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#F3EDE2", "theme_color": "#1F1A16",
		"icons": []map[string]string{
			{"src": "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/static/icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
		"share_target": map[string]any{"action": "/share", "method": "GET",
			"params": map[string]string{"title": "title", "text": "text", "url": "url"}},
	})
}
