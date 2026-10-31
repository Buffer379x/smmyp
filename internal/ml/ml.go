// Package ml: klassisches Machine Learning ohne externe Modelle oder Dienste (Concept 8.2).
// Merkmale, Complement Naive Bayes, SimHash und Spracherkennung, alles in reinem Go.
package ml

import (
	"bytes"
	"encoding/gob"
	"hash/fnv"
	"math"
	"math/bits"
	"math/rand"
	"sort"
	"strings"
	"unicode"
)

// Fold: Kleinschreibung, Umlaute → Grundbuchstaben (ä→a), damit „Übersetzung" und „ubersetzung" gleich sind.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'ä', 'à', 'á', 'â', 'ã', 'å':
			r = 'a'
		case 'ö', 'ò', 'ó', 'ô', 'õ', 'ø':
			r = 'o'
		case 'ü', 'ù', 'ú', 'û':
			r = 'u'
		case 'é', 'è', 'ê', 'ë':
			r = 'e'
		case 'í', 'ì', 'î', 'ï':
			r = 'i'
		case 'ç':
			r = 'c'
		case 'ñ':
			r = 'n'
		case 'ß':
			b.WriteString("ss")
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(stopDE + " " + stopEN) {
		stop[w] = true
	}
}

// Words liefert gefaltete Wörter (Buchstaben/Ziffern) ohne Stoppwörter; Länge ≥ 2.
func Words(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(Fold(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 2 && !stop[w] {
			out = append(out, w)
		}
	}
	return out
}

const featureBits = 20

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32() & (1<<featureBits - 1)
}

// Features: Wörter, Wortpaare und Zeichen-3- bis 5-Gramme langer Wörter (fängt Komposita ab),
// per Feature-Hashing begrenzt, Gewicht log(1+Anzahl).
func Features(text string) map[uint32]float64 {
	words := Words(text)
	counts := map[uint32]float64{}
	for i, w := range words {
		counts[hash("w:"+w)]++
		if i > 0 {
			counts[hash("b:"+words[i-1]+" "+w)] += 0.5
		}
		if r := []rune(w); len(r) >= 7 {
			for n := 3; n <= 5; n++ {
				for j := 0; j+n <= len(r); j++ {
					counts[hash("c:"+string(r[j:j+n]))] += 0.2
				}
			}
		}
	}
	for k, v := range counts {
		counts[k] = math.Log1p(v)
	}
	return counts
}

// ---------- Complement Naive Bayes ----------

// Doc ist ein Trainingsbeispiel mit einem oder mehreren Labels (Tags bzw. Anbieter).
type Doc struct {
	Features map[uint32]float64
	Labels   []string
}

type Scored struct {
	Label string
	Score float64 // 0..1, über 0.5 = deutlich passender als der Durchschnitt
	Z     float64
}

// CNB nach Rennie et al. (2003); gespeichert werden nur die häufigsten Merkmale.
type CNB struct {
	Classes     []string
	ClassTotals []float64
	ClassCounts []map[uint32]float32
	Global      map[uint32]float32
	Total       float64
	Vocab       int
	Alpha       float64
	Samples     int
	Quality     float64 // Trefferquote@3 aus der Kreuzvalidierung
}

const (
	keepPerClass = 2000
	keepGlobal   = 30000
)

// TrainCNB lernt nur Labels mit mindestens minPerClass Beispielen.
func TrainCNB(docs []Doc, minPerClass int) *CNB {
	freq := map[string]int{}
	for _, d := range docs {
		for _, l := range d.Labels {
			freq[l]++
		}
	}
	m := &CNB{Alpha: 1, Global: map[uint32]float32{}}
	idx := map[string]int{}
	for l, n := range freq {
		if n >= minPerClass {
			idx[l] = 0
			m.Classes = append(m.Classes, l)
		}
	}
	sort.Strings(m.Classes)
	for i, c := range m.Classes {
		idx[c] = i
	}
	m.ClassTotals = make([]float64, len(m.Classes))
	m.ClassCounts = make([]map[uint32]float32, len(m.Classes))
	for i := range m.ClassCounts {
		m.ClassCounts[i] = map[uint32]float32{}
	}
	for _, d := range docs {
		var cls []int
		for _, l := range d.Labels {
			if i, ok := idx[l]; ok {
				cls = append(cls, i)
			}
		}
		if len(cls) == 0 {
			continue
		}
		m.Samples++
		for f, v := range d.Features {
			m.Global[f] += float32(v)
			m.Total += v
			for _, c := range cls {
				m.ClassCounts[c][f] += float32(v)
				m.ClassTotals[c] += v
			}
		}
	}
	m.Vocab = len(m.Global)
	m.prune()
	return m
}

func topN(src map[uint32]float32, n int) map[uint32]float32 {
	if len(src) <= n {
		return src
	}
	type kv struct {
		k uint32
		v float32
	}
	all := make([]kv, 0, len(src))
	for k, v := range src {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v || (all[i].v == all[j].v && all[i].k < all[j].k) })
	out := make(map[uint32]float32, n)
	for _, e := range all[:n] {
		out[e.k] = e.v
	}
	return out
}

func (m *CNB) prune() {
	for i := range m.ClassCounts {
		m.ClassCounts[i] = topN(m.ClassCounts[i], keepPerClass)
	}
	g := topN(m.Global, keepGlobal)
	for _, cc := range m.ClassCounts {
		for f := range cc {
			if _, ok := g[f]; !ok {
				g[f] = m.Global[f]
			}
		}
	}
	m.Global = g
}

// Predict liefert alle Klassen absteigend nach Passung.
func (m *CNB) Predict(f map[uint32]float64) []Scored {
	if m == nil || len(m.Classes) < 2 {
		return nil
	}
	v := float64(m.Vocab)
	d := make([]float64, len(m.Classes))
	n := 0.0
	for feat, x := range f {
		g, ok := m.Global[feat]
		if !ok {
			continue
		}
		n += x
		for c := range m.Classes {
			nct := float64(m.ClassCounts[c][feat])
			comp := (float64(g) - nct + m.Alpha) / (m.Total - m.ClassTotals[c] + m.Alpha*v)
			d[c] -= x * math.Log(comp) // höher = passender (Komplement erklärt das Merkmal schlechter)
		}
	}
	if n == 0 {
		return nil
	}
	mean, sd := 0.0, 0.0
	for _, x := range d {
		mean += x
	}
	mean /= float64(len(d))
	for _, x := range d {
		sd += (x - mean) * (x - mean)
	}
	sd = math.Sqrt(sd / float64(len(d)))
	out := make([]Scored, len(d))
	for c, x := range d {
		z := 0.0
		if sd > 0 {
			z = (x - mean) / sd
		}
		out[c] = Scored{Label: m.Classes[c], Z: z, Score: 1 / (1 + math.Exp(-(z - 1.5)))}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Z > out[j].Z || (out[i].Z == out[j].Z && out[i].Label < out[j].Label)
	})
	return out
}

// CrossValidate misst die Trefferquote@3: Anteil der Beispiele mit mindestens einem echten Label unter den Top 3.
func CrossValidate(docs []Doc, minPerClass, folds int) float64 {
	if len(docs) < folds*2 {
		return 0
	}
	hits, total := 0, 0
	// Feste Zufallspermutation: Reihenfolge der Daten (z. B. sortierter Import) verfälscht die Folds nicht.
	perm := rand.New(rand.NewSource(42)).Perm(len(docs))
	for k := 0; k < folds; k++ {
		var train, test []Doc
		for i, d := range docs {
			if perm[i]%folds == k {
				test = append(test, d)
			} else {
				train = append(train, d)
			}
		}
		m := TrainCNB(train, minPerClass)
		known := map[string]bool{}
		for _, c := range m.Classes {
			known[c] = true
		}
		for _, d := range test {
			want := map[string]bool{}
			for _, l := range d.Labels {
				if known[l] {
					want[l] = true
				}
			}
			if len(want) == 0 {
				continue
			}
			total++
			pred := m.Predict(d.Features)
			for i := 0; i < len(pred) && i < 3; i++ {
				if want[pred[i].Label] {
					hits++
					break
				}
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

func (m *CNB) Encode() ([]byte, error) {
	var b bytes.Buffer
	err := gob.NewEncoder(&b).Encode(m)
	return b.Bytes(), err
}

func DecodeCNB(data []byte) (*CNB, error) {
	var m CNB
	err := gob.NewDecoder(bytes.NewReader(data)).Decode(&m)
	return &m, err
}

// ---------- SimHash ----------

// SimHash über Wörter; ähnliche Texte haben einen kleinen Hamming-Abstand.
// Wortpaare wurden bewusst weggelassen: Sie vergrößern den Abstand bei kurzen Prompts zu stark.
func SimHash(text string) uint64 {
	words := Words(text)
	if len(words) == 0 {
		return 0
	}
	var acc [64]int
	add := func(s string, w int) {
		h := fnv.New64a()
		h.Write([]byte(s))
		v := h.Sum64()
		for i := 0; i < 64; i++ {
			if v&(1<<uint(i)) != 0 {
				acc[i] += w
			} else {
				acc[i] -= w
			}
		}
	}
	for _, w := range words {
		add(w, 1)
	}
	var out uint64
	for i := 0; i < 64; i++ {
		if acc[i] > 0 {
			out |= 1 << uint(i)
		}
	}
	if out == 0 {
		out = 1 // 0 bedeutet in der DB „noch nicht berechnet"
	}
	return out
}

func Hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// NearDupDistance: gemessen trennen ≤ 10 Fast-Duplikate (2–8) von verwandten (≈17) und fremden (≈35) Prompts.
const NearDupDistance = 10

func NearDuplicate(a, b uint64) bool { return a != 0 && b != 0 && Hamming(a, b) <= NearDupDistance }

// ---------- Sprache ----------

// Language erkennt Deutsch oder Englisch über Stoppwörter; "" bei zu wenig Text.
func Language(text string) string {
	de, en := 0, 0
	lower := strings.ToLower(text)
	if strings.ContainsAny(lower, "äöüß") {
		de += 2
	}
	for _, w := range strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if deSet[w] {
			de++
		}
		if enSet[w] {
			en++
		}
	}
	switch {
	case de >= 2 && de > en:
		return "de"
	case en >= 2 && en > de:
		return "en"
	}
	return ""
}

var deSet, enSet = map[string]bool{}, map[string]bool{}

func init() {
	for _, w := range strings.Fields(langDE) {
		deSet[w] = true
	}
	for _, w := range strings.Fields(langEN) {
		enSet[w] = true
	}
}

// Kennwörter für die Spracherkennung (häufig, sprachtypisch, wenig Überschneidung).
const langDE = "der die das und ist nicht ein eine einen mit für auf den dem des sich auch von zu im ich du wir sie es wie bitte diesen diese dieser oder aber wenn dann kein keine noch nur schreibe erkläre prüfe gib über"
const langEN = "the and is not with for on of to in you we it this that these are be please write explain check give your from as an or but if then no only about what"

// Stoppwörter (gefaltet) für Merkmale.
const stopDE = "der die das und ist ein eine einen einem einer mit fur auf den dem des sich auch von zu im in ich du wir sie es wie oder aber wenn dann nur noch so als bei nach aus um am an war sind wird werden hat haben kann"
const stopEN = "the and is an a with for on of to in you we it this that these those are be as or but if then so at by from was were has have can will"
