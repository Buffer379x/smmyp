package ml

import (
	"regexp"
	"strconv"
	"strings"
)

// Entry: ein Tag aus dem mitgelieferten Wörterbuch (Stufe 1). Match-Begriffe sind gefaltet;
// mehrteilige Begriffe werden als Phrase gesucht.
type Entry struct {
	Tag   string
	Match []string
	Regex *regexp.Regexp
}

// Dictionary je Anbieter-Kategorie (Concept 8.2).
var Dictionary = map[string][]Entry{
	"musik": {
		{Tag: "synthwave", Match: []string{"synthwave", "retrowave", "outrun"}},
		{Tag: "lo-fi", Match: []string{"lofi", "lo fi"}},
		{Tag: "hip-hop", Match: []string{"hip hop", "hiphop", "boom bap"}},
		{Tag: "techno", Match: []string{"techno"}},
		{Tag: "house", Match: []string{"house", "deep house"}},
		{Tag: "ambient", Match: []string{"ambient", "drone"}},
		{Tag: "jazz", Match: []string{"jazz", "jazzy", "bebop"}},
		{Tag: "rock", Match: []string{"rock", "grunge"}},
		{Tag: "metal", Match: []string{"metal", "metalcore", "djent"}},
		{Tag: "pop", Match: []string{"pop", "synthpop", "electropop"}},
		{Tag: "edm", Match: []string{"edm", "electronic dance"}},
		{Tag: "drum-and-bass", Match: []string{"dnb", "drum and bass", "drum n bass", "jungle"}},
		{Tag: "trap", Match: []string{"trap"}},
		{Tag: "r-n-b", Match: []string{"rnb", "r b", "rhythm and blues"}},
		{Tag: "soul", Match: []string{"soul", "neo soul"}},
		{Tag: "funk", Match: []string{"funk", "funky"}},
		{Tag: "folk", Match: []string{"folk"}},
		{Tag: "country", Match: []string{"country", "bluegrass"}},
		{Tag: "klassik", Match: []string{"classical", "klassik", "baroque", "barock"}},
		{Tag: "orchestral", Match: []string{"orchestral", "orchestra", "orchester", "strings section"}},
		{Tag: "cinematic", Match: []string{"cinematic", "epic", "soundtrack", "film score"}},
		{Tag: "punk", Match: []string{"punk"}},
		{Tag: "reggae", Match: []string{"reggae", "dub"}},
		{Tag: "blues", Match: []string{"blues"}},
		{Tag: "indie", Match: []string{"indie"}},
		{Tag: "chillout", Match: []string{"chillout", "chill", "downtempo"}},
		{Tag: "schlager", Match: []string{"schlager"}},
		{Tag: "akustisch", Match: []string{"acoustic", "akustisch", "unplugged"}},
		{Tag: "instrumental", Match: []string{"instrumental", "no vocals"}},
		{Tag: "weibliche-stimme", Match: []string{"female vocal", "female vocals", "female singer", "weibliche stimme", "sangerin"}},
		{Tag: "maennliche-stimme", Match: []string{"male vocal", "male vocals", "male singer", "mannliche stimme", "sanger"}},
		{Tag: "dark", Match: []string{"dark", "duster", "sinister"}},
		{Tag: "melancholisch", Match: []string{"melancholic", "melancholy", "melancholisch", "sad", "traurig"}},
		{Tag: "energisch", Match: []string{"energetic", "upbeat", "energisch", "driving"}},
		{Tag: "tempo-langsam", Regex: regexp.MustCompile(`\b([4-8]\d)\s?bpm\b`)},
		{Tag: "tempo-mittel", Regex: regexp.MustCompile(`\b(9\d|1[01]\d|120)\s?bpm\b`)},
		{Tag: "tempo-schnell", Regex: regexp.MustCompile(`\b(12[1-9]|1[3-9]\d|2\d\d)\s?bpm\b`)},
	},
	"bild": {
		{Tag: "portrait", Match: []string{"portrait", "portrat", "headshot"}},
		{Tag: "landschaft", Match: []string{"landscape", "landschaft", "scenery"}},
		{Tag: "cinematic", Match: []string{"cinematic", "film still", "movie still"}},
		{Tag: "fotorealistisch", Match: []string{"photorealistic", "fotorealistisch", "35mm", "kodak", "portra", "dslr"}},
		{Tag: "anime", Match: []string{"anime", "manga", "ghibli"}},
		{Tag: "illustration", Match: []string{"illustration", "vector", "flat design", "line art"}},
		{Tag: "3d", Match: []string{"3d render", "octane", "blender", "unreal engine", "cgi"}},
		{Tag: "noir", Match: []string{"noir", "film noir"}},
		{Tag: "aquarell", Match: []string{"watercolor", "watercolour", "aquarell"}},
		{Tag: "logo", Match: []string{"logo", "icon", "emblem"}},
		{Tag: "produktfoto", Match: []string{"product shot", "product photo", "produktfoto", "packshot"}},
		{Tag: "dark", Match: []string{"dark", "moody", "duster"}},
	},
	"video": {
		{Tag: "cinematic", Match: []string{"cinematic", "film look"}},
		{Tag: "zeitlupe", Match: []string{"slow motion", "zeitlupe", "slowmo"}},
		{Tag: "drohne", Match: []string{"drone shot", "aerial", "drohne", "luftaufnahme"}},
	},
	"text": {
		{Tag: "zusammenfassen", Match: []string{"summarize", "summary", "zusammenfassung", "zusammenfassen", "fasse", "tl dr"}},
		{Tag: "uebersetzen", Match: []string{"translate", "translation", "ubersetze", "ubersetzen", "ubersetzung"}},
		{Tag: "code-review", Match: []string{"code review", "review this code", "prufe diesen code", "code-review", "reviewer"}},
		{Tag: "erklaeren", Match: []string{"explain", "erklare", "erklaren", "eli5"}},
		{Tag: "e-mail", Match: []string{"email", "e mail", "mail an", "anschreiben"}},
		{Tag: "brainstorming", Match: []string{"brainstorm", "brainstorming", "ideen", "ideas"}},
		{Tag: "korrektur", Match: []string{"proofread", "korrigiere", "korrektur", "rechtschreibung", "grammar"}},
		{Tag: "recherche", Match: []string{"research", "recherche", "recherchiere", "quellen"}},
		{Tag: "marketing", Match: []string{"marketing", "werbetext", "slogan", "copywriting"}},
	},
	"code": {
		{Tag: "golang", Match: []string{"golang"}},
		{Tag: "python", Match: []string{"python", "django", "pandas"}},
		{Tag: "javascript", Match: []string{"javascript", "node js", "nodejs", "react"}},
		{Tag: "typescript", Match: []string{"typescript"}},
		{Tag: "rust", Match: []string{"rust", "cargo"}},
		{Tag: "sql", Match: []string{"sql", "sqlite", "postgres", "mysql"}},
		{Tag: "shell", Match: []string{"bash", "shell script", "zsh"}},
		{Tag: "docker", Match: []string{"docker", "dockerfile", "compose"}},
		{Tag: "tests", Match: []string{"unit test", "unit tests", "testfalle", "test cases"}},
		{Tag: "refactoring", Match: []string{"refactor", "refactoring", "refaktorieren"}},
		{Tag: "code-review", Match: []string{"code review", "review this", "prufe diesen code"}},
	},
}

// Hit ist ein Treffer im Wörterbuch.
type Hit struct {
	Tag, Because string
}

// MatchDictionary prüft den Text gegen die Wörterbücher der angegebenen Kategorien (leer = alle).
func MatchDictionary(text string, categories []string) []Hit {
	if len(categories) == 0 {
		for c := range Dictionary {
			categories = append(categories, c)
		}
	}
	// Ohne Stoppwort-Filter, damit Phrasen wie „no vocals" erhalten bleiben.
	plain := " " + strings.Join(strings.FieldsFunc(Fold(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r < 128
	}), " ") + " "
	lower := strings.ToLower(text)
	var out []Hit
	seen := map[string]bool{}
	for _, c := range categories {
		for _, e := range Dictionary[c] {
			if seen[e.Tag] {
				continue
			}
			if e.Regex != nil {
				if m := e.Regex.FindString(lower); m != "" {
					seen[e.Tag] = true
					out = append(out, Hit{e.Tag, m})
				}
				continue
			}
			for _, w := range e.Match {
				if strings.Contains(plain, " "+w+" ") {
					seen[e.Tag] = true
					out = append(out, Hit{e.Tag, w})
					break
				}
			}
		}
	}
	return out
}

var arRe = regexp.MustCompile(`--ar\s+(\d+)\s*:\s*(\d+)`)

// AspectTag leitet aus Midjourney-Parametern das Bildformat ab.
func AspectTag(text string) string {
	m := arRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	switch {
	case w > h:
		return "querformat"
	case h > w:
		return "hochformat"
	case w > 0:
		return "quadratisch"
	}
	return ""
}
