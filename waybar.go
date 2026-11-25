package main

import (
	"encoding/json"
	"strings"
)

// waybarOut is one line for a Waybar custom module ("return-type": "json").
type waybarOut struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
	Alt     string `json:"alt"`
}

func pango(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Waybar builds the module output. Class is ok, note, warn or bad, so the
// bar can colour it with plain CSS.
func Waybar(fs []Finding) string {
	class, attention := "ok", 0
	for _, f := range fs {
		switch f.Sev {
		case Bad:
			class = "bad"
			attention++
		case Warn:
			if class != "bad" {
				class = "warn"
			}
			attention++
		case Info:
			if class == "ok" {
				class = "note"
			}
		}
	}
	o := waybarOut{Class: class, Alt: class}
	switch {
	case attention > 0:
		o.Text = "● " + itoa(attention)
	case class == "note":
		o.Text = "●"
	default:
		o.Text = "● " + L("ok", "tamam")
	}
	if len(fs) == 0 {
		o.Tooltip = L("Everything looks fine", "Her şey yolunda")
	}
	var lines []string
	for _, f := range fs {
		lines = append(lines, "<b>"+pango(f.Title)+"</b>")
		for _, e := range f.Evidence {
			lines = append(lines, "  › "+pango(e))
		}
		lines = append(lines, "")
	}
	if len(lines) > 0 {
		o.Tooltip = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	b, _ := json.Marshal(o)
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
