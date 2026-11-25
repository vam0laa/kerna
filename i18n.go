package main

import (
	"os"
	"strconv"
	"strings"
)

var lang = detectLang()

func detectLang() string {
	for _, k := range []string{"KERNA_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return strings.ToLower(v[:min(2, len(v))])
		}
	}
	return "en"
}

// L picks the Turkish text when the language is Turkish, English otherwise.
func L(en, tr string) string {
	if lang == "tr" {
		return tr
	}
	return en
}

// fl formats a number with d decimals, using a comma when the language is Turkish.
func fl(v float64, d int) string {
	s := strconv.FormatFloat(v, 'f', d, 64)
	if lang == "tr" {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}
