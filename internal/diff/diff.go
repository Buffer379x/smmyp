// Package diff vergleicht Texte wortweise (für Prompt-Versionen).
package diff

import "regexp"

type Kind int

const (
	Equal Kind = iota
	Delete
	Insert
)

type Op struct {
	Kind Kind
	Text string
}

var tokenRe = regexp.MustCompile(`\s+|[\p{L}\p{N}_]+|[^\s\p{L}\p{N}_]`)

// maxCells begrenzt die LCS-Tabelle; größere Texte werden als Ganzes ersetzt angezeigt.
const maxCells = 4_000_000

// Words liefert die Änderungen von a nach b auf Wortebene (Leerraum bleibt erhalten).
func Words(a, b string) []Op {
	if a == b {
		if a == "" {
			return nil
		}
		return []Op{{Equal, a}}
	}
	x, y := tokenRe.FindAllString(a, -1), tokenRe.FindAllString(b, -1)
	n, m := len(x), len(y)
	if n*m > maxCells {
		return compact([]Op{{Delete, a}, {Insert, b}})
	}
	// lcs[i][j] = Länge der längsten gemeinsamen Teilfolge von x[i:] und y[j:]
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []Op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			ops = append(ops, Op{Equal, x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, Op{Delete, x[i]})
			i++
		default:
			ops = append(ops, Op{Insert, y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, Op{Delete, x[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, Op{Insert, y[j]})
	}
	return compact(ops)
}

// compact fasst aufeinanderfolgende Operationen gleicher Art zusammen.
func compact(ops []Op) []Op {
	var out []Op
	for _, o := range ops {
		if o.Text == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Kind == o.Kind {
			out[len(out)-1].Text += o.Text
			continue
		}
		out = append(out, o)
	}
	return out
}

// Changed meldet, ob es Unterschiede gibt.
func Changed(ops []Op) bool {
	for _, o := range ops {
		if o.Kind != Equal {
			return true
		}
	}
	return false
}
