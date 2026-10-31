package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Buffer379x/smmyp/internal/auth"
	"github.com/Buffer379x/smmyp/internal/prompts"
	"github.com/Buffer379x/smmyp/internal/providers"
	"github.com/Buffer379x/smmyp/internal/search"
	"github.com/Buffer379x/smmyp/internal/tags"
)

// Rail: Schubladen links (Desktop) bzw. Chips oben (Handy).
type Rail struct {
	AllCount, Favorites, Trash int
	Categories                 []RailCategory
	All                        []providers.Provider
	TopTags                    []tags.Count
	Active                     string // Slug der aktiven Schublade, "fav" oder ""
}

type RailCategory struct {
	Label  string
	Drawer []RailDrawer
}

type RailDrawer struct {
	Slug, Name, Color string
	Count             int
}

func (s *Server) rail(r *http.Request, u *auth.User) (*Rail, error) {
	ctx := r.Context()
	all, err := providers.List(ctx, s.db, u.Scope())
	if err != nil {
		return nil, err
	}
	counts, err := s.prompts.Counts(ctx, u.Scope())
	if err != nil {
		return nil, err
	}
	top, err := tags.Top(ctx, s.db.R, u.ID, 12)
	if err != nil {
		return nil, err
	}
	rail := &Rail{AllCount: counts.All, Favorites: counts.Favorites, Trash: counts.Trash, All: all, TopTags: top}
	var cur *RailCategory
	for _, p := range all {
		n := counts.ByProvider[p.ID]
		if n == 0 {
			continue // leere Schubladen werden ausgeblendet
		}
		label := providers.CategoryLabel(p.Category)
		if cur == nil || cur.Label != label {
			rail.Categories = append(rail.Categories, RailCategory{Label: label})
			cur = &rail.Categories[len(rail.Categories)-1]
		}
		cur.Drawer = append(cur.Drawer, RailDrawer{Slug: p.Slug, Name: p.Name, Color: p.Color, Count: n})
	}
	return rail, nil
}

func scopeOptions(all []providers.Provider, current string) []ScopeOption {
	opts := []ScopeOption{{Value: "", Label: "Alle KIs", Selected: current == ""}}
	for _, c := range providers.Categories {
		v := "k:" + c.Key
		opts = append(opts, ScopeOption{Value: v, Label: "Alle " + c.Label, Group: "Kategorien", Selected: current == v})
	}
	for _, p := range all {
		opts = append(opts, ScopeOption{Value: p.Slug, Label: p.Name, Group: providers.CategoryLabel(p.Category), Selected: current == p.Slug})
	}
	return opts
}

type TabVM struct{ Name, Color string }

type FieldVM struct {
	Key, Label string
	Value      template.HTML
	Raw        string
	Primary    bool
}

type TagVM struct {
	Slug string
	Hit  bool
}

type CardVM struct {
	ID          int64
	Title       template.HTML
	TitleRaw    string
	URL         string
	Tabs        []TabVM
	MoreTabs    int
	Model       string
	LastUsed    int64
	UseCount    int
	Fields      []FieldVM
	Tags        []TagVM
	Favorite    bool
	ContextSlug string
	Snippet     template.HTML
	PrimaryRaw  string
	Primary     string
	Names       string
	Deleted     int64
}

// card baut die Ansicht eines Prompts. ctx ist der Anbieter der aktuellen Schublade (oder nil).
func card(p prompts.Prompt, ctx *providers.Provider, q search.Query) CardVM {
	terms := q.HighlightTerms()
	c := CardVM{ID: p.ID, TitleRaw: p.Title, Title: search.Highlight(p.Title, terms), URL: "/p/" + strconv.FormatInt(p.ID, 10),
		Model: p.Model, LastUsed: p.LastUsedAt, UseCount: p.UseCount, Favorite: p.IsFavorite, Deleted: p.DeletedAt}
	for i, l := range p.Providers {
		if i == 3 && len(p.Providers) > 4 {
			c.MoreTabs = len(p.Providers) - 3
			break
		}
		c.Tabs = append(c.Tabs, TabVM{Name: l.Name, Color: l.Color})
	}
	if len(p.Providers) > 0 {
		c.Primary = p.Providers[0].Name
	}
	var names []string
	for _, l := range p.Providers {
		names = append(names, l.Name)
	}
	c.Names = strings.Join(names, ", ")
	// Kontext: Felder und Primärfeld des Schubladen-Anbieters, sonst Union mit dem Hauptanbieter vorn.
	var fields []providers.Field
	primaryKey := ""
	if ctx != nil {
		if l, ok := p.Provider(ctx.ID); ok {
			fields = l.Fields
			primaryKey = l.PrimaryField
			c.ContextSlug = l.Slug
			c.UseCount = l.UseCount
			c.LastUsed = l.LastUsedAt
			c.URL += "?a=" + url.QueryEscape(l.Slug)
		}
	}
	if fields == nil {
		fields = providers.Union(p.ProviderList())
		primaryKey = p.Primary().PrimaryField
	}
	for _, f := range fields {
		v := p.Fields[f.Key]
		if v == "" {
			continue
		}
		c.Fields = append(c.Fields, FieldVM{Key: f.Key, Label: f.Label, Raw: v, Value: search.Highlight(v, terms), Primary: f.Key == primaryKey})
	}
	hasPrimary := false
	for _, f := range c.Fields {
		hasPrimary = hasPrimary || f.Primary
	}
	if !hasPrimary && len(c.Fields) > 0 {
		c.Fields[0].Primary = true
	}
	for _, f := range c.Fields {
		if f.Primary {
			snip := []rune(strings.Join(strings.Fields(f.Raw), " "))
			if len(snip) > 160 {
				snip = append(snip[:160], '…')
			}
			c.Snippet = search.Highlight(string(snip), terms)
			c.PrimaryRaw = f.Raw
		}
	}
	for _, t := range p.Tags {
		hit := false
		for _, qt := range q.Tags {
			hit = hit || qt == t
		}
		c.Tags = append(c.Tags, TagVM{Slug: t, Hit: hit})
	}
	return c
}

type RefVM struct {
	ID       int64
	Title    template.HTML
	URL      string
	ShownIn  string
	Provider string
}

type GroupVM struct {
	Slug, Name, Color string
	Count             int
	Cards             []CardVM
	Refs              []RefVM
	MoreURL           string
}

// group ordnet Prompts nach KI. Ein Prompt mit mehreren Anbietern erscheint als volle Karte nur in der
// ersten passenden Gruppe, in weiteren als Verweiszeile (Concept 7.4).
func group(items []prompts.Prompt, order []providers.Provider, only map[int64]bool, facet map[int64]int, perGroup int, q search.Query, rawQ string) []GroupVM {
	shownIn := map[int64]string{}
	var groups []GroupVM
	for i := range order {
		prov := order[i]
		if only != nil && !only[prov.ID] {
			continue
		}
		g := GroupVM{Slug: prov.Slug, Name: prov.Name, Color: prov.Color, Count: facet[prov.ID]}
		n := 0
		for _, p := range items {
			if _, ok := p.Provider(prov.ID); !ok {
				continue
			}
			if n >= perGroup {
				break
			}
			n++
			if where, ok := shownIn[p.ID]; ok {
				g.Refs = append(g.Refs, RefVM{ID: p.ID, Title: search.Highlight(p.Title, q.HighlightTerms()), URL: "/p/" + strconv.FormatInt(p.ID, 10) + "?a=" + prov.Slug, ShownIn: where, Provider: prov.Name})
				continue
			}
			shownIn[p.ID] = prov.Name
			g.Cards = append(g.Cards, card(p, &prov, q))
		}
		if n == 0 {
			continue
		}
		if g.Count > n {
			v := url.Values{"a": {prov.Slug}}
			if rawQ != "" {
				v.Set("q", rawQ)
			}
			g.MoreURL = "/?" + v.Encode()
		}
		groups = append(groups, g)
	}
	return groups
}
