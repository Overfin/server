package tmdb

import "testing"

// FuzzEnglishNames: whatever TMDB says, a name in Latin letters is never
// replaced, and an English name is only ever taken in Latin letters.
func FuzzEnglishNames(f *testing.F) {
	f.Add("尚公主", "尚公主", "The Princess Royal")
	f.Add("La casa de papel", "La casa de papel", "Money Heist")
	f.Add("小城良方", "小城良方", "")
	f.Add("Отец", "Отец", "Отец")
	f.Fuzz(func(t *testing.T, name, original, english string) {
		r := rawTitle{Name: name, OriginalName: original}
		was := r.unreadable()
		r.english(rawTitle{Name: english})
		switch {
		case latin(name) && r.Name != name:
			t.Errorf("a Latin name %q replaced by %q", name, r.Name)
		case r.Name != name && !latin(r.Name):
			t.Errorf("%q replaced by %q, not in Latin letters", name, r.Name)
		case r.Name != name && !was:
			t.Errorf("%q replaced by %q though it was readable", name, r.Name)
		}
	})
}
