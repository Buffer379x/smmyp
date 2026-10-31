// Package vars wertet Platzhalter in Prompts aus: {{name}} und {{name=Standardwert}}.
// \{{ steht für ein wörtliches {{ (Concept R15: Prompts dürfen selbst Templates enthalten).
package vars

import (
	"regexp"
	"strings"
)

// Pattern muss mit app.js übereinstimmen.
var re = regexp.MustCompile(`\\?\{\{\s*([\p{L}\p{N}_][\p{L}\p{N}_ \-]{0,39}?)\s*(?:=([^{}]{0,200}))?\}\}`)

type Var struct {
	Name    string
	Default string
}

// Parse liefert die Platzhalter in Reihenfolge des ersten Auftretens (ohne Duplikate).
func Parse(texts ...string) []Var {
	var out []Var
	seen := map[string]bool{}
	for _, text := range texts {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if strings.HasPrefix(m[0], `\`) {
				continue
			}
			name := strings.TrimSpace(m[1])
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Var{Name: name, Default: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// Has meldet, ob mindestens ein (nicht escapter) Platzhalter vorkommt.
func Has(texts ...string) bool { return len(Parse(texts...)) > 0 }

// Fill ersetzt Platzhalter durch Werte (leer → Standardwert) und macht \{{ zu {{.
func Fill(text string, values map[string]string) string {
	return re.ReplaceAllStringFunc(text, func(m string) string {
		if strings.HasPrefix(m, `\`) {
			return m[1:]
		}
		sub := re.FindStringSubmatch(m)
		name := strings.TrimSpace(sub[1])
		if v, ok := values[name]; ok && v != "" {
			return v
		}
		return strings.TrimSpace(sub[2])
	})
}
