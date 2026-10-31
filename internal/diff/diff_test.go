package diff

import (
	"reflect"
	"testing"
)

func TestWords(t *testing.T) {
	got := Words("dark synthwave, 98 bpm", "dark retrowave, 98 bpm, female vocals")
	want := []Op{{Equal, "dark "}, {Delete, "synthwave"}, {Insert, "retrowave"}, {Equal, ", 98 bpm"}, {Insert, ", female vocals"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if Changed(Words("a b", "a b")) || !Changed(Words("", "x")) || Words("", "") != nil {
		t.Fatal("Changed/leer falsch")
	}
}
