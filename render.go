package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type palette struct{ on bool }

func (p palette) c(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func summary(r Report) []string {
	st := r.Static
	avail := 0.0
	if st.MemTotalKiB > 0 {
		avail = float64(st.MemAvailKiB) / float64(st.MemTotalKiB) * 100
	}
	line1 := fmt.Sprintf("CPU %2.0f%%   load %.1f/%d   RAM %.0f%% free", r.CPUBusy*100, st.Load1, st.CPUs, avail)
	if st.SwapTotalKiB > 0 {
		line1 += fmt.Sprintf("   swap %s used", mib(st.SwapTotalKiB-st.SwapFreeKiB))
	}
	line2 := L("stalled: ", "bekleme: ") + fmt.Sprintf("cpu %.0f%%  disk %.0f%%  memory %.0f%%", r.PSI["cpu"].SomeAvg10, r.PSI["io"].SomeAvg10, r.PSI["memory"].SomeAvg10)
	if st.MaxTempC > 0 {
		line2 += fmt.Sprintf("   %.0f °C", st.MaxTempC)
	}
	out := []string{line1, line2}
	if b := st.Battery; b != nil {
		l := fmt.Sprintf("%s %d%% %s", b.Name, b.Percent, strings.ToLower(b.Status))
		if b.Watts > 0 {
			l += fmt.Sprintf(", %.1f W", b.Watts)
			if b.HoursLeft > 0 && strings.EqualFold(b.Status, "Discharging") {
				l += fmt.Sprintf(", ~%.1f h", b.HoursLeft)
			}
		}
		out = append(out, l)
	}
	for _, g := range st.GPUs {
		if g.Runtime != "" {
			out = append(out, fmt.Sprintf("GPU %s: %s", g.Vendor, g.Runtime))
		}
	}
	return out
}

// Render writes the report. It is plain text without colour when pal.on is false.
func Render(w io.Writer, pal palette, r Report, fs []Finding) {
	fmt.Fprintln(w, pal.c("1;36", "cause")+pal.c("2", "  "+L("why is my system like this", "sistem neden böyle")))
	fmt.Fprintln(w)
	for _, l := range summary(r) {
		fmt.Fprintln(w, "  "+pal.c("2", l))
	}
	fmt.Fprintln(w)
	if len(fs) == 0 {
		fmt.Fprintln(w, pal.c("1;32", "  ✔ ")+pal.c("1", L("Nothing is wrong right now.", "Şu an bir sorun yok.")))
		fmt.Fprintln(w, pal.c("2", "    "+L(fmt.Sprintf("Nothing was waiting for the CPU, disk or memory in the last %.0f seconds, and nothing is overheating or wasting power.", r.Window.Seconds()+8),
			fmt.Sprintf("Son %.0f saniyede hiçbir şey işlemciyi, diski ya da belleği beklemedi; aşırı ısınan ya da güç harcayan bir şey de yok.", r.Window.Seconds()+8))))
		return
	}
	for _, f := range fs {
		mark, col := "ℹ", "1;34"
		switch f.Sev {
		case Warn:
			mark, col = "⚠", "1;33"
		case Bad:
			mark, col = "✖", "1;31"
		}
		fmt.Fprintln(w, pal.c(col, "  "+mark+" ")+pal.c("1", f.Title))
		fmt.Fprintln(w, "    "+f.Why)
		for _, e := range f.Evidence {
			fmt.Fprintln(w, pal.c("36", "      • ")+e)
		}
		if f.Hint != "" {
			fmt.Fprintln(w, pal.c("2", "    → "+f.Hint))
		}
		fmt.Fprintln(w)
	}
}
