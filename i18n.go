package main

import (
	"os"
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
