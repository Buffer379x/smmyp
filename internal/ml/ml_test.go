package ml

import (
	"fmt"
	"math/rand"
	"testing"
)

// Synthetischer Korpus: jedes Genre hat typisches Vokabular plus Rauschen.
func corpus(n int, seed int64) []Doc {
	r := rand.New(rand.NewSource(seed))
	genres := map[string][]string{
		"synthwave": {"neon", "retro", "analog", "pads", "gated", "reverb", "night", "drive", "80s", "arpeggio"},
		"lo-fi":     {"vinyl", "crackle", "mellow", "dusty", "study", "rain", "jazzy", "chords", "tape", "cozy"},
		"metal":     {"distorted", "guitars", "double", "kick", "growl", "riffs", "aggressive", "breakdown", "heavy", "drop"},
		"klassik":   {"strings", "piano", "orchestra", "sonata", "violin", "cello", "baroque", "adagio", "chamber", "harpsichord"},
		"ambient":   {"drone", "atmospheric", "slow", "evolving", "textures", "ethereal", "space", "calm", "pad", "field"},
	}
	noise := []string{"song", "track", "music", "vibe", "style", "sound", "warm", "bright", "deep", "wide", "modern", "classic"}
	var names []string
	for g := range genres {
		names = append(names, g)
	}
	var docs []Doc
	for i := 0; i < n; i++ {
		g := names[(i*7+int(seed))%len(names)]
		words := ""
		for k := 0; k < 6; k++ {
			words += genres[g][r.Intn(10)] + " "
		}
		for k := 0; k < 4; k++ {
			words += noise[r.Intn(len(noise))] + " "
		}
		labels := []string{g}
		if r.Intn(4) == 0 {
			labels = append(labels, "favorit-"+fmt.Sprint(r.Intn(3)))
		}
		docs = append(docs, Doc{Features: Features(words), Labels: labels})
	}
	return docs
}

// TC-ML-02: Trefferquote@3 auf zurückgehaltenen Daten ≥ 0,6.
func TestCNBQualityTC_ML_02(t *testing.T) {
	train, test := corpus(500, 1), corpus(100, 2)
	m := TrainCNB(train, 5)
	hits := 0
	for _, d := range test {
		pred := m.Predict(d.Features)
		for i := 0; i < 3 && i < len(pred); i++ {
			if pred[i].Label == d.Labels[0] {
				hits++
				break
			}
		}
	}
	rate := float64(hits) / float64(len(test))
	if rate < 0.6 {
		t.Fatalf("Trefferquote@3 = %.2f", rate)
	}
	cv := CrossValidate(train, 5, 5)
	t.Logf("Testdaten %.2f, Kreuzvalidierung %.2f, Klassen %v", rate, cv, m.Classes)
	if cv < 0.6 {
		t.Fatalf("Kreuzvalidierung %.2f", cv)
	}
	// Top-1 für einen eindeutigen Text
	if p := m.Predict(Features("dusty vinyl crackle, mellow jazzy chords for study")); p[0].Label != "lo-fi" || p[0].Score < 0.5 {
		t.Fatalf("Top-1: %+v", p[:2])
	}
	// Speichern/Laden ergibt dieselben Vorhersagen.
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := DecodeCNB(b)
	if err != nil || m2.Predict(Features("heavy distorted riffs"))[0].Label != m.Predict(Features("heavy distorted riffs"))[0].Label {
		t.Fatalf("Decode: %v", err)
	}
	if len(b) > 2<<20 {
		t.Fatalf("Modell zu groß: %d Bytes", len(b))
	}
}

func TestCNBTooFewClasses(t *testing.T) {
	if p := TrainCNB(corpus(10, 3), 50).Predict(Features("neon")); p != nil {
		t.Fatal("ohne Klassen darf es keine Vorhersage geben")
	}
}

func TestSimHashTC_ML_07(t *testing.T) {
	a := SimHash("dark synthwave with analog pads, gated reverb drums, breathy female vocals, night city, 98 bpm")
	b := SimHash("dark synthwave with analog pads, gated reverb drums, breathy female vocals, night town, 98 bpm")
	c := SimHash("Prüfe diesen Go-Code auf Nebenläufigkeitsfehler und schlage Tests vor")
	if !NearDuplicate(a, b) {
		t.Errorf("fast gleich nicht erkannt (Abstand %d)", Hamming(a, b))
	}
	if NearDuplicate(a, c) {
		t.Errorf("verschiedene Texte als Duplikat erkannt (Abstand %d)", Hamming(a, c))
	}
}

func TestLanguage(t *testing.T) {
	for text, want := range map[string]string{
		"Prüfe diesen Code und erkläre mir die Fehler":  "de",
		"Please explain the code and check it for bugs": "en",
		"dark synthwave 98 bpm":                         "",
	} {
		if got := Language(text); got != want {
			t.Errorf("%q → %q, erwartet %q", text, got, want)
		}
	}
}

// TC-TAG-04 und Wörterbuch-Treffer.
func TestDictionaryTC_TAG_04(t *testing.T) {
	hits := MatchDictionary("Dark synthwave, 98 bpm, breathy female vocals, no vocals in the intro", []string{"musik"})
	got := map[string]string{}
	for _, h := range hits {
		got[h.Tag] = h.Because
	}
	for _, tag := range []string{"tempo-mittel", "synthwave", "weibliche-stimme", "dark", "instrumental"} {
		if _, ok := got[tag]; !ok {
			t.Errorf("Tag %s fehlt: %v", tag, got)
		}
	}
	if got["tempo-mittel"] != "98 bpm" {
		t.Errorf("Begründung: %q", got["tempo-mittel"])
	}
	if hits := MatchDictionary("120 bpm", []string{"musik"}); len(hits) != 1 || hits[0].Tag != "tempo-mittel" {
		t.Errorf("120 bpm: %+v", hits)
	}
	if hits := MatchDictionary("Übersetze den Text ins Englische", []string{"text"}); len(hits) != 1 || hits[0].Tag != "uebersetzen" {
		t.Errorf("Übersetzen: %+v", hits)
	}
	if AspectTag("--ar 4:5 --style raw") != "hochformat" || AspectTag("--ar 16:9") != "querformat" || AspectTag("x") != "" {
		t.Error("AspectTag")
	}
}
