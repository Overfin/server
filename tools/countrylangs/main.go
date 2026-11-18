// Command countrylangs writes internal/lang/countries.go: each country's
// languages an operator can offer, and their names. It isn't part of
// Overfin's images; run it again only to move to a newer CLDR or when
// TMDB's translations change.
//
// A country's languages are those Unicode CLDR says are official there
// (official, de facto or regional), the largest share of people first,
// kept when TMDB has translations in that language for that country
// (its primary translations, such as "fr-CA") and Overfin knows the
// language (lang.Base). Each one's name is in itself: its language's as
// Overfin writes it, its country's from CLDR: "Deutsch (Schweiz)".
//
// The CLDR packages are cldr-json's, from npm, at cldrVersion exactly;
// TMDB's list is GET /3/configuration/primary_translations, saved as it
// came. From the repository's root:
//
//	npm pack cldr-core@49.0.0 cldr-localenames-full@49.0.0   (then untar each)
//	curl -H "Authorization: Bearer $TOKEN" https://api.themoviedb.org/3/configuration/primary_translations > tmdb.json
//	go run ./tools/countrylangs -core cldr-core/package -names cldr-localenames-full/package -tmdb tmdb.json -fetched YYYY-MM-DD
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/lang"
)

// cldrVersion is the CLDR release the data comes from: the packages read
// must be this one, so the file's header says the truth.
const cldrVersion = "49.0.0"

// official are CLDR's statuses that count as a country's own language.
var official = map[string]bool{"official": true, "de_facto_official": true, "official_regional": true}

func main() {
	core := flag.String("core", "", "the cldr-core package's folder")
	names := flag.String("names", "", "the cldr-localenames-full package's folder")
	tmdbFile := flag.String("tmdb", "", "TMDB's primary_translations, as JSON")
	fetched := flag.String("fetched", "", "the day TMDB's list was fetched, YYYY-MM-DD")
	out := flag.String("o", "internal/lang/countries.go", "the Go file to write")
	flag.Parse()
	if *core == "" || *names == "" || *tmdbFile == "" || *fetched == "" {
		flag.Usage()
		os.Exit(2)
	}
	for _, dir := range []string{*core, *names} {
		checkVersion(dir)
	}

	var tmdbTags []string
	readJSON(*tmdbFile, &tmdbTags)
	tmdb := map[string]bool{}
	for _, t := range tmdbTags {
		tmdb[t] = true
	}

	var info struct {
		Supplemental struct {
			TerritoryInfo map[string]struct {
				LanguagePopulation map[string]struct {
					Percent string `json:"_populationPercent"`
					Status  string `json:"_officialStatus"`
				} `json:"languagePopulation"`
			} `json:"territoryInfo"`
		} `json:"supplemental"`
	}
	readJSON(filepath.Join(*core, "supplemental", "territoryInfo.json"), &info)

	countries := map[string][]string{}
	tagNames := map[string]string{}
	for country, t := range info.Supplemental.TerritoryInfo {
		type found struct {
			tag, code string // code: CLDR's, such as "zh_Hant"
			percent   float64
		}
		var list []found
		for code, l := range t.LanguagePopulation {
			if !official[l.Status] {
				continue
			}
			// "zh_Hant" is Chinese, "fil" Tagalog, as Overfin and TMDB
			// write them.
			base := lang.Base(strings.Split(code, "_")[0])
			tag := base + "-" + country
			if base == "" || !tmdb[tag] {
				continue
			}
			p, err := strconv.ParseFloat(l.Percent, 64)
			if err != nil {
				log.Fatalf("%s in %s: share %q: %v", code, country, l.Percent, err)
			}
			i := slices.IndexFunc(list, func(f found) bool { return f.tag == tag })
			switch {
			case i < 0:
				list = append(list, found{tag, code, p})
			case p > list[i].percent:
				list[i] = found{tag, code, p}
			}
		}
		slices.SortFunc(list, func(a, b found) int {
			if a.percent != b.percent {
				if a.percent > b.percent {
					return -1
				}
				return 1
			}
			return strings.Compare(a.tag, b.tag)
		})
		for _, f := range list {
			countries[country] = append(countries[country], f.tag)
			tagNames[f.tag] = nameOf(*names, f.code, country)
		}
	}

	var b bytes.Buffer
	b.WriteString(strings.Replace(header, "FETCHED", *fetched, 1))
	b.WriteString("var countryLanguages = map[string][]string{\n")
	for _, c := range sortedKeys(countries) {
		fmt.Fprintf(&b, "\t%q: {", c)
		for i, tag := range countries[c] {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", tag)
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n\n// countryNames are those languages' names, each in itself.\nvar countryNames = map[string]string{\n")
	for _, tag := range sortedKeys(tagNames) {
		fmt.Fprintf(&b, "\t%q: %q,\n", tag, tagNames[tag])
	}
	b.WriteString("}\n")
	src, err := format.Source(b.Bytes())
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d countries, %d languages\n", len(countries), len(tagNames))
}

// nameOf is a language's name in a country, in that language: the
// language's as Overfin writes it (lang.Name, "Français"), the country's
// from CLDR, from the locale for its script when CLDR has one ("zh-Hant"
// for Taiwan), else its language's ("zh").
func nameOf(dir, code, country string) string {
	base, _, _ := strings.Cut(code, "_")
	locale := strings.ReplaceAll(code, "_", "-")
	if _, err := os.Stat(filepath.Join(dir, "main", locale)); err != nil {
		locale = base
	}
	var territories struct {
		Main map[string]struct {
			Names struct {
				Territories map[string]string `json:"territories"`
			} `json:"localeDisplayNames"`
		} `json:"main"`
	}
	readJSON(filepath.Join(dir, "main", locale, "territories.json"), &territories)
	t := territories.Main[locale].Names.Territories[country]
	if t == "" {
		log.Fatalf("CLDR names no %s in %s", country, locale)
	}
	return lang.Name(base) + " (" + t + ")"
}

func checkVersion(dir string) {
	var p struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	readJSON(filepath.Join(dir, "package.json"), &p)
	if p.Version != cldrVersion {
		log.Fatalf("%s is CLDR %s, not %s: change cldrVersion (and the header) to move", p.Name, p.Version, cldrVersion)
	}
}

func readJSON(path string, v any) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		log.Fatalf("%s: %v", path, err)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// header starts the file written: where its data comes from, how to make
// it again, and the notice CLDR's license asks for.
const header = `// Code generated by tools/countrylangs; DO NOT EDIT.

// Each country's languages an operator can offer, and their names: the
// languages Unicode CLDR ` + cldrVersion + ` (supplemental/territoryInfo.json,
// cldr-core on npm) says are official there, the largest share of people
// first, that TMDB has translations in for that country
// (GET /3/configuration/primary_translations, fetched FETCHED). The
// countries' names in them from CLDR ` + cldrVersion + ` too (cldr-localenames-full).
// To make it again, see tools/countrylangs.
//
// CLDR's data is under the Unicode License v3:
//
// UNICODE LICENSE V3
//
// COPYRIGHT AND PERMISSION NOTICE
//
// Copyright © 2001–2026 Unicode, Inc.
//
// NOTICE TO USER: Carefully read the following legal agreement. BY
// DOWNLOADING, INSTALLING, COPYING OR OTHERWISE USING DATA FILES, AND/OR
// SOFTWARE, YOU UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY, ALL OF THE
// TERMS AND CONDITIONS OF THIS AGREEMENT. IF YOU DO NOT AGREE, DO NOT
// DOWNLOAD, INSTALL, COPY, DISTRIBUTE OR USE THE DATA FILES OR SOFTWARE.
//
// Permission is hereby granted, free of charge, to any person obtaining a
// copy of data files and any associated documentation (the "Data Files") or
// software and any associated documentation (the "Software") to deal in the
// Data Files or Software without restriction, including without limitation
// the rights to use, copy, modify, merge, publish, distribute, and/or sell
// copies of the Data Files or Software, and to permit persons to whom the
// Data Files or Software are furnished to do so, provided that either (a)
// this copyright and permission notice appear with all copies of the Data
// Files or Software, or (b) this copyright and permission notice appear in
// associated Documentation.
//
// THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
// KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF
// THIRD PARTY RIGHTS.
//
// IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS NOTICE
// BE LIABLE FOR ANY CLAIM, OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL DAMAGES,
// OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS,
// WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION,
// ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THE DATA
// FILES OR SOFTWARE.
//
// Except as contained in this notice, the name of a copyright holder shall
// not be used in advertising or otherwise to promote the sale, use or other
// dealings in these Data Files or Software without prior written
// authorization of the copyright holder.

package lang

// countryLanguages are each country's languages, by its ISO 3166 code.
`
