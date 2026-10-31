// Package search enthält den Such-Parser, den Aufbau sicherer FTS5-Ausdrücke und das Hervorheben.
// Es greift nicht auf die Datenbank zu und ist deshalb vollständig per Unit-Test prüfbar.
package search

import (
	"html"
	"html/template"
	"strconv"
	"strings"
	"unicode"

	"github.com/Buffer379x/smmyp/internal/tags"
)

// Query ist eine geparste Sucheingabe.
type Query struct {
	Terms      []string // Präfixsuche, UND-verknüpft
	Phrases    []string // exakte Phrasen
	Excludes   []string // -wort
	Providers  []string // @slug, ODER-verknüpft
	Categories []string // kat:musik
	Tags       []string // #tag, UND-verknüpft (normalisiert)
	Favorites  bool     // fav:
	UsedDays   int      // used:7d
}

// Empty: keine Volltextsuche nötig (nur Filter oder gar nichts).
func (q Query) HasText() bool { return len(q.Terms)+len(q.Phrases) > 0 }

func (q Query) IsZero() bool {
	return !q.HasText() && len(q.Excludes)+len(q.Providers)+len(q.Categories)+len(q.Tags) == 0 && !q.Favorites && q.UsedDays == 0
}

// HighlightTerms liefert die Begriffe, die in Treffern markiert werden.
func (q Query) HighlightTerms() []string {
	out := append([]string{}, q.Terms...)
	return append(out, q.Phrases...)
}

const maxTokens = 32

// Parse zerlegt die Eingabe. Unbekannte Sonderzeichen werden ignoriert, nie als Operator interpretiert.
func Parse(input string) Query {
	var q Query
	input = strings.TrimSpace(input)
	if len(input) > 500 {
		input = input[:500]
	}
	tokens := 0
	for len(input) > 0 && tokens < maxTokens {
		input = strings.TrimLeftFunc(input, unicode.IsSpace)
		if input == "" {
			break
		}
		tokens++
		if input[0] == '"' {
			end := strings.IndexByte(input[1:], '"')
			var phrase string
			if end < 0 {
				phrase, input = input[1:], ""
			} else {
				phrase, input = input[1:end+1], input[end+2:]
			}
			if p := strings.Join(strings.Fields(phrase), " "); hasWordChar(p) {
				q.Phrases = append(q.Phrases, p)
			}
			continue
		}
		i := strings.IndexFunc(input, unicode.IsSpace)
		var tok string
		if i < 0 {
			tok, input = input, ""
		} else {
			tok, input = input[:i], input[i:]
		}
		lower := strings.ToLower(tok)
		switch {
		case strings.HasPrefix(tok, "@") && len(tok) > 1:
			if s := slug(tok[1:]); s != "" {
				q.Providers = appendUnique(q.Providers, s)
			}
		case strings.HasPrefix(tok, "#") && len(tok) > 1:
			if t := tags.Normalize(tok[1:]); t != "" {
				q.Tags = appendUnique(q.Tags, t)
			}
		case strings.HasPrefix(lower, "kat:") && len(tok) > 4:
			if s := slug(tok[4:]); s != "" {
				q.Categories = appendUnique(q.Categories, s)
			}
		case lower == "fav:" || lower == "fav":
			if lower == "fav:" {
				q.Favorites = true
			} else {
				q.Terms = appendUnique(q.Terms, "fav")
			}
		case strings.HasPrefix(lower, "used:"):
			v := strings.TrimSuffix(lower[5:], "d")
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 3650 {
				q.UsedDays = n
			}
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			if w := cleanWord(tok[1:]); w != "" {
				q.Excludes = appendUnique(q.Excludes, w)
			}
		default:
			if w := cleanWord(tok); w != "" {
				q.Terms = appendUnique(q.Terms, w)
			}
		}
	}
	return q
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

// cleanWord entfernt FTS-Sonderzeichen am Rand; Wörter ohne Buchstaben/Ziffern fallen weg.
func cleanWord(s string) string {
	s = strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if !hasWordChar(s) {
		return ""
	}
	return s
}

func hasWordChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// quote macht aus beliebigem Text einen FTS5-String; innere Anführungszeichen werden verdoppelt.
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// MatchExpr baut den positiven FTS5-Ausdruck (leer, wenn keine Textbegriffe vorhanden sind).
func (q Query) MatchExpr() string {
	var parts []string
	for _, t := range q.Terms {
		parts = append(parts, quote(t)+"*")
	}
	for _, p := range q.Phrases {
		parts = append(parts, quote(p))
	}
	return strings.Join(parts, " AND ")
}

// ExcludeExpr baut den Ausdruck für auszuschließende Wörter (ODER-verknüpft).
func (q Query) ExcludeExpr() string {
	var parts []string
	for _, e := range q.Excludes {
		parts = append(parts, quote(e))
	}
	return strings.Join(parts, " OR ")
}

// String setzt eine Query wieder als Eingabetext zusammen (für Links).
func (q Query) String() string {
	var parts []string
	for _, p := range q.Providers {
		parts = append(parts, "@"+p)
	}
	for _, c := range q.Categories {
		parts = append(parts, "kat:"+c)
	}
	for _, t := range q.Tags {
		parts = append(parts, "#"+t)
	}
	if q.Favorites {
		parts = append(parts, "fav:")
	}
	if q.UsedDays > 0 {
		parts = append(parts, "used:"+strconv.Itoa(q.UsedDays)+"d")
	}
	parts = append(parts, q.Terms...)
	for _, p := range q.Phrases {
		parts = append(parts, `"`+p+`"`)
	}
	for _, e := range q.Excludes {
		parts = append(parts, "-"+e)
	}
	return strings.Join(parts, " ")
}

// fold vergleicht ohne Groß-/Kleinschreibung und ohne Akzente, Rune für Rune (Positionen bleiben gleich).
func fold(r rune) rune {
	r = unicode.ToLower(r)
	switch r {
	case 'ä', 'à', 'á', 'â', 'ã', 'å', 'ā':
		return 'a'
	case 'ö', 'ò', 'ó', 'ô', 'õ', 'ø', 'ō':
		return 'o'
	case 'ü', 'ù', 'ú', 'û', 'ū':
		return 'u'
	case 'é', 'è', 'ê', 'ë', 'ē':
		return 'e'
	case 'í', 'ì', 'î', 'ï':
		return 'i'
	case 'ç':
		return 'c'
	case 'ñ':
		return 'n'
	}
	return r
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Highlight escaped text vollständig und markiert danach Treffer mit <mark>.
// Begriffe treffen am Wortanfang (wie die Präfixsuche), Phrasen mit Leerzeichen ebenso.
func Highlight(text string, terms []string) template.HTML {
	if len(terms) == 0 || text == "" {
		return template.HTML(html.EscapeString(text))
	}
	src := []rune(text)
	folded := make([]rune, len(src))
	for i, r := range src {
		folded[i] = fold(r)
	}
	marked := make([]bool, len(src))
	for _, t := range terms {
		needle := []rune(strings.ToLower(t))
		for i := range needle {
			needle[i] = fold(needle[i])
		}
		if len(needle) == 0 {
			continue
		}
		for i := 0; i+len(needle) <= len(folded); i++ {
			if i > 0 && isWord(folded[i-1]) {
				continue
			}
			match := true
			for j, r := range needle {
				if folded[i+j] != r {
					match = false
					break
				}
			}
			if match {
				for j := range needle {
					marked[i+j] = true
				}
			}
		}
	}
	var b strings.Builder
	in := false
	for i, r := range src {
		if marked[i] && !in {
			b.WriteString("<mark>")
			in = true
		} else if !marked[i] && in {
			b.WriteString("</mark>")
			in = false
		}
		b.WriteString(html.EscapeString(string(r)))
	}
	if in {
		b.WriteString("</mark>")
	}
	return template.HTML(b.String())
}
