package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- colour -----------------------------------------------------------

type palette struct{ on bool }

// 256-colour codes: they work in every modern terminal.
const (
	cAccent = "38;5;39"
	cOK     = "38;5;78"
	cWarn   = "38;5;214"
	cBad    = "38;5;203"
	cDim    = "2" // faint: follows the terminal theme, so it is readable on light and dark
	cText   = ""  // the terminal's own foreground
	cBold   = "1"
)

func (p palette) c(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) sev(s Severity) string {
	switch s {
	case Bad:
		return cBad
	case Warn:
		return cWarn
	}
	return cOK
}

func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ---- text helpers -----------------------------------------------------

func vlen(s string) int { return utf8.RuneCountInString(s) }

func wrap(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && vlen(cur)+1+vlen(w) > width {
			lines = append(lines, cur)
			cur = w
		} else if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func clip(s string, width int) string {
	if vlen(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

func pad(s string, n int) string {
	if v := vlen(s); v < n {
		return s + strings.Repeat(" ", n-v)
	}
	return s
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// spark draws the last `width` values on a fixed 0..max scale.
func spark(vals []float64, width int, min, max float64) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var b strings.Builder
	n := len(vals)
	for _, v := range vals {
		i := 0
		if max > 0 {
			i = int(v/max*float64(len(sparkRunes)-1) + 0.5)
		}
		if i < 0 {
			i = 0
		}
		if i >= len(sparkRunes) {
			i = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[i])
	}
	b.WriteString(strings.Repeat(" ", width-n))
	return b.String()
}

// gauge is the one-shot counterpart of spark: a filled bar.
func gauge(v, min, max float64, width int) (full, empty string) {
	n := 0
	if max > min {
		n = int((v-min)/(max-min)*float64(width) + 0.5)
	}
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n), strings.Repeat("░", width-n)
}

// ---- history of the live view ----------------------------------------

type Hist struct{ CPU, PCPU, PIO, Mem, Temp, Watts []float64 }

const histCap = 120

func push(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > histCap {
		s = s[len(s)-histCap:]
	}
	return s
}

func (h *Hist) Add(r Report) {
	st := r.Static
	h.CPU = push(h.CPU, r.CPUBusy*100)
	h.PCPU = push(h.PCPU, r.PSI["cpu"].SomeAvg10)
	h.PIO = push(h.PIO, r.PSI["io"].SomeAvg10)
	if st.MemTotalKiB > 0 {
		h.Mem = push(h.Mem, 100-float64(st.MemAvailKiB)/float64(st.MemTotalKiB)*100)
	}
	h.Temp = push(h.Temp, st.MaxTempC)
	if st.Battery != nil && strings.EqualFold(st.Battery.Status, "Discharging") {
		h.Watts = push(h.Watts, st.Battery.Watts)
	}
}

// ---- the screen --------------------------------------------------------

type metric struct {
	label  string
	series []float64
	min    float64
	max    float64
	value  string
	note   string
	sev    Severity
	last   float64
}

func stallSev(v float64) Severity {
	switch {
	case v >= psiBad:
		return Bad
	case v >= psiWarn:
		return Warn
	}
	return Info
}

func metrics(r Report, h *Hist) []metric {
	st := r.Static
	last := func(s []float64, v float64) []float64 {
		if len(s) == 0 {
			return []float64{v}
		}
		return s
	}
	usedPct := 0.0
	if st.MemTotalKiB > 0 {
		usedPct = 100 - float64(st.MemAvailKiB)/float64(st.MemTotalKiB)*100
	}
	cpuSev := Info
	if r.CPUBusy >= 0.9 {
		cpuSev = Warn
	}
	memSev := Info
	switch {
	case usedPct >= 96:
		memSev = Bad
	case usedPct >= 90:
		memSev = Warn
	}
	ms := []metric{
		{label: L("Processor", "İşlemci"), series: last(h.CPU, r.CPUBusy*100), max: 100, value: fmt.Sprintf("%.0f%%", r.CPUBusy*100), sev: cpuSev},
		{label: L("CPU wait", "CPU bekleme"), series: last(h.PCPU, r.PSI["cpu"].SomeAvg10), max: 50, value: fmt.Sprintf("%.0f%%", r.PSI["cpu"].SomeAvg10), sev: stallSev(r.PSI["cpu"].SomeAvg10)},
		{label: L("Disk wait", "Disk bekleme"), series: last(h.PIO, r.PSI["io"].SomeAvg10), max: 50, value: fmt.Sprintf("%.0f%%", r.PSI["io"].SomeAvg10), sev: stallSev(r.PSI["io"].SomeAvg10)},
		{label: L("Memory", "Bellek"), series: last(h.Mem, usedPct), max: 100, value: fmt.Sprintf("%.0f%% %s", usedPct, L("used", "dolu")), sev: memSev},
	}
	if st.MaxTempC > 0 {
		tsev := Info
		switch {
		case st.MaxTempC >= hotC:
			tsev = Bad
		case st.MaxTempC >= 80:
			tsev = Warn
		}
		ms = append(ms, metric{label: L("Temperature", "Sıcaklık"), series: last(h.Temp, st.MaxTempC), min: 30, max: 100, value: fmt.Sprintf("%.0f °C", st.MaxTempC), sev: tsev})
	}
	if b := st.Battery; b != nil {
		note := fmt.Sprintf("%d%%", b.Percent)
		if strings.EqualFold(b.Status, "Discharging") {
			wsev := Info
			if b.HoursLeft > 0 {
				note += fmt.Sprintf(", %s h %s", fl(b.HoursLeft, 1), L("left", "kaldı"))
			}
			if b.Watts >= drainWatts {
				wsev = Warn
			}
			if st.Profile != "" {
				note += ", " + L("profile ", "profil ") + st.Profile
			}
			mx := 60.0
			if b.Watts > mx {
				mx = b.Watts
			}
			ms = append(ms, metric{label: L("Power", "Güç"), series: last(h.Watts, b.Watts), max: mx, value: fl(b.Watts, 1) + " W", note: note, sev: wsev})
		} else {
			// While charging the kernel reports how fast the battery fills, not what the system uses.
			note += ", " + L("charging", "şarj oluyor")
			if strings.EqualFold(b.Status, "Full") {
				note = fmt.Sprintf("%d%%, %s", b.Percent, L("full", "dolu"))
			}
			ms = append(ms, metric{label: L("Charging", "Şarj"), series: []float64{b.Watts}, max: 140, value: "+" + fl(b.Watts, 1) + " W", note: note, sev: Info})
		}
	}
	return ms
}

func gpuLine(st Static) string {
	var parts []string
	for _, g := range st.GPUs {
		s := gpuName(g.Vendor)
		switch {
		case g.BusyPct >= 0:
			s += fmt.Sprintf(" %d%%", g.BusyPct)
		case g.Runtime == "suspended":
			s += " " + L("asleep", "uyuyor")
		case g.Runtime == "active":
			s += " " + L("awake", "uyanık")
		}
		if g.Watts >= 1 {
			s += fmt.Sprintf(" %.0f W", g.Watts)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "  ·  ")
}

func worstOf(fs []Finding) Severity {
	w := Info
	for _, f := range fs {
		if f.Sev > w {
			w = f.Sev
		}
	}
	return w
}

func rule(p palette, title string, width int) string {
	t := "── " + title + " "
	return p.c(cDim, t+strings.Repeat("─", max(0, width-vlen(t))))
}

func verdict(p palette, fs []Finding) string {
	attention, notes := 0, 0
	worst := Info
	for _, f := range fs {
		if f.Sev == Info {
			notes++
			continue
		}
		attention++
		if f.Sev > worst {
			worst = f.Sev
		}
	}
	noteTxt := ""
	if notes > 0 {
		noteTxt = p.c(cDim, L(fmt.Sprintf("  ·  %d note", notes), fmt.Sprintf("  ·  %d not", notes)))
		if notes > 1 && lang != "tr" {
			noteTxt = p.c(cDim, fmt.Sprintf("  ·  %d notes", notes))
		}
	}
	if attention == 0 {
		return p.c(cOK, "●") + " " + p.c(cBold+";"+cOK, L("Everything looks fine", "Her şey yolunda")) + noteTxt
	}
	col := cWarn
	if worst == Bad {
		col = cBad
	}
	txt := L("1 thing needs attention", "1 şey dikkat istiyor")
	if attention > 1 {
		txt = L(fmt.Sprintf("%d things need attention", attention), fmt.Sprintf("%d şey dikkat istiyor", attention))
	}
	return p.c(col, "●") + " " + p.c(cBold+";"+col, txt) + noteTxt
}

// Opts are the interactive extras of the live view.
type Opts struct {
	Sel int  // index of the selected finding; -1 for none
	Top bool // show the list of busiest programs
}

// Frame builds the whole screen as lines. width is the usable width.
func Frame(p palette, r Report, fs []Finding, h *Hist, width int, host string, now time.Time, note string, live bool) []string {
	return FrameOpts(p, r, fs, h, width, host, now, note, live, Opts{Sel: -1})
}

// FrameOpts is Frame with the interactive extras.
func FrameOpts(p palette, r Report, fs []Finding, h *Hist, width int, host string, now time.Time, note string, live bool, o Opts) []string {
	if width > 96 {
		width = 96
	}
	if width < 56 {
		width = 56
	}
	var out []string
	head := p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("why is my system like this", "sistem neden böyle"))
	right := host + "  ·  " + now.Format("15:04:05")
	if note != "" {
		right = note + "  ·  " + right
	}
	gap := width - vlen("kerna  "+L("why is my system like this", "sistem neden böyle")) - vlen(right)
	if gap < 2 {
		gap = 2
	}
	out = append(out, head+strings.Repeat(" ", gap)+p.c(cDim, right))
	out = append(out, "")
	out = append(out, verdict(p, fs))
	out = append(out, "")

	const labelW, sparkW = 13, 30
	for _, m := range metrics(r, h) {
		col := p.sev(m.sev)
		if m.sev == Info {
			col = cOK
		}
		var viz string
		if live {
			viz = p.c(col, spark(m.series, sparkW, m.min, m.max))
		} else {
			full, empty := gauge(m.series[len(m.series)-1], m.min, m.max, sparkW)
			viz = p.c(col, full) + p.c(cDim, empty)
		}
		line := "  " + p.c(cDim, pad(m.label, labelW)) + viz + "  " + p.c(cText, pad(m.value, 9))
		if m.note != "" {
			line += p.c(cDim, m.note)
		}
		out = append(out, line)
	}
	if r.NetRxBps+r.NetTxBps > 0 || r.Static.WifiIface != "" {
		line := fmt.Sprintf("↓ %s  ↑ %s", human(r.NetRxBps), human(r.NetTxBps))
		if r.Static.WifiIface != "" {
			line += fmt.Sprintf("   Wi-Fi %d dBm", r.Static.WifiDBm)
		}
		out = append(out, "  "+p.c(cDim, pad(L("Network", "Ağ"), labelW))+line)
	}
	if top := r.GroupCPU(3); len(top) > 0 && top[0].CPU >= 1 {
		var parts []string
		for _, g := range top {
			if g.CPU < 1 {
				break
			}
			n := g.Comm
			if g.Count > 1 {
				n += fmt.Sprintf(" ×%d", g.Count)
			}
			parts = append(parts, fmt.Sprintf("%s %.0f%%", n, g.CPU))
		}
		out = append(out, "  "+p.c(cDim, pad(L("Busiest", "En yoğun"), labelW))+strings.Join(parts, p.c(cDim, "  ·  ")))
	}
	if g := gpuLine(r.Static); g != "" {
		out = append(out, "  "+p.c(cDim, pad(L("Graphics", "Grafik"), labelW))+p.c(cText, g))
	}
	out = append(out, "")
	if o.Top {
		out = append(out, rule(p, L("Programs", "Programlar"), width), "")
		out = append(out, p.c(cDim, fmt.Sprintf("  %-40s %8s %10s %9s", "", "CPU", L("wake-ups/s", "uyanma/sn"), "RAM")))
		for _, g := range r.GroupCPU(8) {
			if g.CPU < 0.5 && g.Wakeups < 20 {
				continue
			}
			name := g.Comm
			if g.Count > 1 {
				name += fmt.Sprintf(" ×%d", g.Count)
			}
			if d := describe(g.Comm); d != "" {
				name += p.c(cDim, " · "+d)
			}
			out = append(out, "  "+pad(clip(name, 40), 40)+fmt.Sprintf(" %7s%% %10s %9s", fl(g.CPU, 0), fl(g.Wakeups, 0), mib(g.RSSKiB)))
		}
		out = append(out, "")
	}

	if len(fs) > 0 {
		title := L("What is wrong", "Neyin yanlış olduğu")
		if worstOf(fs) == Info {
			title = L("Notes", "Notlar")
		}
		out = append(out, rule(p, title, width), "")
	}
	for i, f := range fs {
		col := cOK
		mark := "●"
		if o.Sel == i && len(fs) > 1 {
			mark = "▸"
		}
		switch f.Sev {
		case Warn:
			col = cWarn
		case Bad:
			col = cBad
		case Info:
			col = cAccent
		}
		out = append(out, "  "+p.c(col, mark)+" "+p.c(cBold, f.Title))
		for _, l := range wrap(f.Why, width-4) {
			out = append(out, "    "+p.c(cText, l))
		}
		for _, e := range f.Evidence {
			ls := wrap(e, width-8)
			for i, l := range ls {
				lead := "      "
				if i == 0 {
					lead = "    " + p.c(cAccent, "› ")
				} else {
					lead = "      "
				}
				out = append(out, lead+p.c(cText, l))
			}
		}
		if f.Hint != "" {
			cmd, comment, _ := strings.Cut(f.Hint, "   # ")
			out = append(out, "    "+p.c(cDim, "$ ")+p.c(cBold, cmd))
			for _, l := range wrap(comment, width-6) {
				out = append(out, "      "+p.c(cDim, l))
			}
		}
		out = append(out, "")
	}
	if len(fs) == 0 {
		msg := L(fmt.Sprintf("Nothing was waiting for the CPU, disk or memory in the last %.0f seconds, and nothing is overheating or wasting power.", r.Window.Seconds()+8),
			fmt.Sprintf("Son %.0f saniyede hiçbir şey işlemciyi, diski ya da belleği beklemedi; aşırı ısınan ya da güç harcayan bir şey de yok.", r.Window.Seconds()+8))
		for _, l := range wrap(msg, width-4) {
			out = append(out, "  "+p.c(cDim, l))
		}
		out = append(out, "")
	}
	return out
}

// HistoryLines renders the incident list for `kerna history` and the h key.
func HistoryLines(p palette, es []Entry, span time.Duration, width int) []string {
	var out []string
	out = append(out, p.c(cBold+";"+cAccent, "kerna")+p.c(cDim, "  "+L("history", "geçmiş")), "")
	if len(es) == 0 {
		out = append(out, "  "+L("No history yet.", "Henüz geçmiş yok."))
		out = append(out, p.c(cDim, "  "+L("Start recording with: kerna service install", "Kaydı başlatmak için: kerna service install")))
		return out
	}
	ins := Incidents(es)
	hours := span.Hours()
	if len(ins) == 0 {
		out = append(out, "  "+p.c(cOK, "●")+" "+p.c(cBold, L(fmt.Sprintf("No problems in the last %.0f hours.", hours), fmt.Sprintf("Son %.0f saatte sorun yok.", hours))))
		out = append(out, p.c(cDim, "  "+L(fmt.Sprintf("%d samples, from %s.", len(es), es[0].T.Format("Mon 15:04")), fmt.Sprintf("%d örnek, %s tarihinden.", len(es), es[0].T.Format("Mon 15:04")))))
		return out
	}
	out = append(out, p.c(cDim, "  "+L(fmt.Sprintf("Last %.0f hours, %d problems:", hours, len(ins)), fmt.Sprintf("Son %.0f saat, %d sorun:", hours, len(ins)))), "")
	lastDay := ""
	for _, in := range ins {
		day := in.From.Format("Mon 02 Jan")
		if day != lastDay {
			out = append(out, "  "+p.c(cDim, day))
			lastDay = day
		}
		col := cWarn
		if in.Sev == Bad {
			col = cBad
		} else if in.Sev == Info {
			col = cAccent
		}
		span := in.From.Format("15:04") + "–" + in.To.Add(recordEvery).Format("15:04")
		line := "    " + p.c(cText, pad(span, 12)) + p.c(col, "●") + " " + titleOf(in.ID)
		if in.Peak != "" {
			line += p.c(cDim, "  "+in.Peak)
		}
		out = append(out, line)
		if in.Evidence != "" {
			out = append(out, "                   "+p.c(cDim, clip(in.Evidence, width-21)))
		}
	}
	out = append(out, "", p.c(cDim, "  "+L("Details of one moment: kerna history --at 03:12", "Bir anın ayrıntısı: kerna history --at 03:12")))
	return out
}

// MomentLines renders one stored sample for `kerna history --at`.
func MomentLines(p palette, e Entry, width int) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+e.T.Format("Mon 02 Jan 15:04:05")), ""}
	out = append(out, fmt.Sprintf("  CPU %.0f%%   %s: cpu %.0f%%  disk %.0f%%  %s %.0f%%", e.CPU, L("stalled", "bekleme"), e.PCPU, e.PIO, L("memory", "bellek"), e.PMem))
	extra := ""
	if e.Temp > 0 {
		extra += fmt.Sprintf("%.0f °C", e.Temp)
	}
	if e.Watts > 0 {
		extra += "   " + fl(e.Watts, 1) + " W"
	}
	if extra != "" {
		out = append(out, "  "+extra)
	}
	out = append(out, "")
	if len(e.Findings) == 0 {
		return append(out, "  "+p.c(cOK, "●")+" "+L("Nothing was wrong at that moment.", "O anda bir sorun yoktu."))
	}
	for _, f := range e.Findings {
		col := cWarn
		if f.Sev == Bad {
			col = cBad
		} else if f.Sev == Info {
			col = cAccent
		}
		out = append(out, "  "+p.c(col, "●")+" "+p.c(cBold, titleOf(f.ID)))
		for _, ev := range f.Evidence {
			for i, l := range wrap(ev, width-8) {
				lead := "      "
				if i == 0 {
					lead = "    " + p.c(cAccent, "› ")
				}
				out = append(out, lead+l)
			}
		}
		out = append(out, "")
	}
	return out
}
