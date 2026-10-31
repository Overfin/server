package lang

import (
	"slices"
	"strings"
)

// Languages' names: each in itself, the same on the site, in the player,
// in the control panel and through the API. A variant an operator can
// offer (Choices) has its country: "Français (Canada)"; any other, its
// language's name alone.

// language is one language: its two-letter code (ISO 639-1, or TMDB's own
// for Cantonese), its three-letter ones (ISO 639-2, bibliographic and
// terminology, as files write them), and its name in itself.
type language struct {
	two   string
	three []string
	name  string
}

// languages are those films and series come in, and their subtitles.
var languages = []language{
	{"af", []string{"afr"}, "Afrikaans"},
	{"am", []string{"amh"}, "አማርኛ"},
	{"ar", []string{"ara"}, "العربية"},
	{"az", []string{"aze"}, "Azərbaycan dili"},
	{"be", []string{"bel"}, "Беларуская"},
	{"bg", []string{"bul"}, "Български"},
	{"bn", []string{"ben"}, "বাংলা"},
	{"bo", []string{"bod", "tib"}, "བོད་སྐད་"},
	{"br", []string{"bre"}, "Brezhoneg"},
	{"bs", []string{"bos"}, "Bosanski"},
	{"ca", []string{"cat"}, "Català"},
	{"cn", []string{"yue"}, "粵語"}, // Cantonese: TMDB writes it cn
	{"co", []string{"cos"}, "Corsu"},
	{"cs", []string{"ces", "cze"}, "Čeština"},
	{"cy", []string{"cym", "wel"}, "Cymraeg"},
	{"da", []string{"dan"}, "Dansk"},
	{"de", []string{"deu", "ger"}, "Deutsch"},
	{"el", []string{"ell", "gre"}, "Ελληνικά"},
	{"en", []string{"eng"}, "English"},
	{"eo", []string{"epo"}, "Esperanto"},
	{"es", []string{"spa"}, "Español"},
	{"et", []string{"est"}, "Eesti"},
	{"eu", []string{"eus", "baq"}, "Euskara"},
	{"fa", []string{"fas", "per"}, "فارسی"},
	{"fi", []string{"fin"}, "Suomi"},
	{"fo", []string{"fao"}, "Føroyskt"},
	{"fr", []string{"fra", "fre"}, "Français"},
	{"ga", []string{"gle"}, "Gaeilge"},
	{"gd", []string{"gla"}, "Gàidhlig"},
	{"gl", []string{"glg"}, "Galego"},
	{"gu", []string{"guj"}, "ગુજરાતી"},
	{"ha", []string{"hau"}, "Hausa"},
	{"he", []string{"heb"}, "עברית"},
	{"hi", []string{"hin"}, "हिन्दी"},
	{"hr", []string{"hrv"}, "Hrvatski"},
	{"ht", []string{"hat"}, "Kreyòl ayisyen"},
	{"hu", []string{"hun"}, "Magyar"},
	{"hy", []string{"hye", "arm"}, "Հայերեն"},
	{"id", []string{"ind"}, "Bahasa Indonesia"},
	{"ig", []string{"ibo"}, "Igbo"},
	{"is", []string{"isl", "ice"}, "Íslenska"},
	{"it", []string{"ita"}, "Italiano"},
	{"iu", []string{"iku"}, "ᐃᓄᒃᑎᑐᑦ"},
	{"ja", []string{"jpn"}, "日本語"},
	{"jv", []string{"jav"}, "Basa Jawa"},
	{"ka", []string{"kat", "geo"}, "ქართული"},
	{"kk", []string{"kaz"}, "Қазақ тілі"},
	{"km", []string{"khm"}, "ខ្មែរ"},
	{"kn", []string{"kan"}, "ಕನ್ನಡ"},
	{"ko", []string{"kor"}, "한국어"},
	{"ku", []string{"kur"}, "Kurdî"},
	{"ky", []string{"kir"}, "Кыргызча"},
	{"la", []string{"lat"}, "Latina"},
	{"lb", []string{"ltz"}, "Lëtzebuergesch"},
	{"lo", []string{"lao"}, "ລາວ"},
	{"lt", []string{"lit"}, "Lietuvių"},
	{"lv", []string{"lav"}, "Latviešu"},
	{"mi", []string{"mri", "mao"}, "Māori"},
	{"mk", []string{"mkd", "mac"}, "Македонски"},
	{"ml", []string{"mal"}, "മലയാളം"},
	{"mn", []string{"mon"}, "Монгол"},
	{"mr", []string{"mar"}, "मराठी"},
	{"ms", []string{"msa", "may"}, "Bahasa Melayu"},
	{"mt", []string{"mlt"}, "Malti"},
	{"my", []string{"mya", "bur"}, "မြန်မာ"},
	{"nb", []string{"nob"}, "Norsk bokmål"},
	{"ne", []string{"nep"}, "नेपाली"},
	{"nl", []string{"nld", "dut"}, "Nederlands"},
	{"nn", []string{"nno"}, "Norsk nynorsk"},
	{"no", []string{"nor"}, "Norsk"},
	{"oc", []string{"oci"}, "Occitan"},
	{"pa", []string{"pan"}, "ਪੰਜਾਬੀ"},
	{"pl", []string{"pol"}, "Polski"},
	{"ps", []string{"pus"}, "پښتو"},
	{"pt", []string{"por"}, "Português"},
	{"ro", []string{"ron", "rum"}, "Română"},
	{"ru", []string{"rus"}, "Русский"},
	{"sa", []string{"san"}, "संस्कृतम्"},
	{"si", []string{"sin"}, "සිංහල"},
	{"sk", []string{"slk", "slo"}, "Slovenčina"},
	{"sl", []string{"slv"}, "Slovenščina"},
	{"so", []string{"som"}, "Soomaali"},
	{"sq", []string{"sqi", "alb"}, "Shqip"},
	{"sr", []string{"srp"}, "Српски"},
	{"sv", []string{"swe"}, "Svenska"},
	{"sw", []string{"swa"}, "Kiswahili"},
	{"ta", []string{"tam"}, "தமிழ்"},
	{"te", []string{"tel"}, "తెలుగు"},
	{"tg", []string{"tgk"}, "Тоҷикӣ"},
	{"th", []string{"tha"}, "ไทย"},
	{"tk", []string{"tuk"}, "Türkmençe"},
	{"tl", []string{"tgl", "fil"}, "Tagalog"},
	{"tr", []string{"tur"}, "Türkçe"},
	{"uk", []string{"ukr"}, "Українська"},
	{"ur", []string{"urd"}, "اردو"},
	{"uz", []string{"uzb"}, "Oʻzbekcha"},
	{"vi", []string{"vie"}, "Tiếng Việt"},
	{"xh", []string{"xho"}, "isiXhosa"},
	{"yi", []string{"yid"}, "ייִדיש"},
	{"yo", []string{"yor"}, "Yorùbá"},
	{"zh", []string{"zho", "chi"}, "中文"},
	{"zu", []string{"zul"}, "isiZulu"},
}

// byTwo and threeToTwo find a language by its codes.
var (
	byTwo      = map[string]language{}
	threeToTwo = map[string]string{}
)

func init() {
	for _, l := range languages {
		byTwo[l.two] = l
		for _, t := range l.three {
			threeToTwo[t] = l.two
		}
	}
}

// Tag is a language as Overfin writes it, however it came: its two-letter
// code, and its region when it has one ("fr", "fr-CA", "es-419"), from
// "fre", "fr_ca", "FR-CA"…; "" for a language it doesn't know.
func Tag(s string) string {
	// The language as Base reads it; the region, what follows it to the
	// next separator.
	base := Base(s)
	if base == "" {
		return ""
	}
	t := strings.TrimSpace(s)
	if i := strings.IndexAny(t, "-_"); i >= 0 {
		r := t[i+1:]
		if j := strings.IndexAny(r, "-_"); j >= 0 {
			r = r[:j]
		}
		switch {
		case len(r) == 2 && isLetters(r):
			return base + "-" + strings.ToUpper(r)
		case len(r) == 3 && isDigits(r):
			return base + "-" + r
		}
	}
	return base
}

// Name is a language's name in itself: "Français (Canada)" for a variant
// an operator can offer, "Français" for the language alone or a variant
// it can't; a language Overfin doesn't know, its code as written (a code
// only: letters, 2 or 3); none, or one said unknown, "?".
func Name(s string) string {
	t := Tag(s)
	if t == "" {
		code := strings.ToLower(strings.TrimSpace(s))
		if (len(code) == 2 || len(code) == 3) && isLetters(code) && !unknown[code] {
			return code
		}
		return "?"
	}
	for _, c := range Choices {
		if c.Tag == t {
			return c.Name
		}
	}
	if n, ok := variants[t]; ok {
		return n
	}
	return byTwo[Base(t)].name
}

// variants are the regional variants files tell apart (see the tracks
// package's markers) that aren't among the Choices.
var variants = map[string]string{"es-419": "Español (Latinoamérica)"}

// List is every language with its name, for a choice of languages: each
// language alone, and its variants (Choices and those files tell apart)
// after it, in the order of their names.
func List() []Choice {
	var out []Choice
	for _, l := range languages {
		out = append(out, Choice{l.two, l.name})
		var vs []Choice
		for _, c := range Choices {
			if Base(c.Tag) == l.two {
				vs = append(vs, c)
			}
		}
		for tag, name := range variants {
			if Base(tag) == l.two {
				vs = append(vs, Choice{tag, name})
			}
		}
		slices.SortFunc(vs, func(a, b Choice) int { return strings.Compare(a.Name, b.Name) })
		out = append(out, vs...)
	}
	slices.SortStableFunc(out, func(a, b Choice) int {
		return strings.Compare(byTwo[Base(a.Tag)].name, byTwo[Base(b.Tag)].name)
	})
	return out
}

// unknown are the codes files use for no language in particular.
var unknown = map[string]bool{"und": true, "mis": true, "mul": true, "zxx": true, "qaa": true}

func isLetters(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return s != ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
