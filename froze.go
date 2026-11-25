package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// `kerna why-froze` is the one command that runs another program: journalctl,
// read-only, to read the kernel and system log of the previous and the current
// boot. Everything else in kerna reads only /proc and /sys.

var journal = func(args ...string) (string, error) {
	out, err := exec.Command("journalctl", append([]string{"--no-pager", "-o", "short-iso"}, args...)...).Output()
	return string(out), err
}

type kind struct {
	id    string
	title [2]string
	re    *regexp.Regexp
}

var kinds = []kind{
	{"oom", [2]string{"Memory ran out and a program was killed", "Bellek bitti ve bir program öldürüldü"},
		regexp.MustCompile(`(?i)out of memory: killed process|invoked oom-killer|oom-kill:|systemd-oomd.*killed|killed .* due to memory pressure`)},
	{"gpu", [2]string{"The graphics driver hung or was reset", "Ekran kartı sürücüsü takıldı ya da sıfırlandı"},
		regexp.MustCompile(`(?i)amdgpu.*(gpu reset|ring \S+ timeout|ring timeout)|NVRM: Xid|i915.*(gpu hang|resetting)|xe .*gt reset`)},
	{"thermal", [2]string{"The CPU overheated or was throttled", "İşlemci aşırı ısındı ya da yavaşlatıldı"},
		regexp.MustCompile(`(?i)(core |package )?temperature above threshold|thermal.*(critical|shutdown)|cpu clock throttled`)},
	{"disk", [2]string{"The disk reported errors", "Disk hata bildirdi"},
		regexp.MustCompile(`(?i)blk_update_request: i/o error|nvme\d+: .*(timeout|reset controller)|ext4-fs error|btrfs: .*(error|csum)`)},
	{"hung", [2]string{"A task was blocked for a long time", "Bir görev uzun süre takılı kaldı"},
		regexp.MustCompile(`(?i)blocked for more than \d+ seconds`)},
	{"panic", [2]string{"A kernel panic or hardware error", "Çekirdek paniği ya da donanım hatası"},
		regexp.MustCompile(`(?i)kernel panic|machine check|mce: .*(hardware error|fatal)`)},
}

// A normal shutdown leaves these in the log; a freeze or a power loss does not.
var cleanEnd = regexp.MustCompile(`(?i)reached target (system )?(power-?off|reboot|halt|final step|shutdown)|systemd-shutdown|shutting down|journal stopped|stopped target|unmounted /`)

// Event is one log line that matched.
type Event struct {
	T    time.Time
	Kind string
	Line string
}

func parseJournalTime(line string) (time.Time, bool) {
	f, _, _ := strings.Cut(line, " ")
	for _, layout := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, f); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ScanEvents finds the interesting lines of one boot's log text.
func ScanEvents(text string) []Event {
	var out []Event
	for _, line := range strings.Split(text, "\n") {
		t, ok := parseJournalTime(line)
		if !ok {
			continue
		}
		for _, k := range kinds {
			if k.re.MatchString(line) {
				if n := len(out); n > 0 && out[n-1].Kind == k.id && t.Sub(out[n-1].T) < 5*time.Second {
					break // the same burst
				}
				out = append(out, Event{T: t, Kind: k.id, Line: shorten(line)})
				break
			}
		}
	}
	return out
}

// shorten drops the timestamp and host so the line fits on a screen.
func shorten(line string) string {
	parts := strings.SplitN(line, " ", 3)
	s := line
	if len(parts) == 3 {
		s = parts[2]
	}
	if len(s) > 150 {
		s = s[:149] + "…"
	}
	return s
}

// EndOfBoot reports whether the log of a boot ends with a normal shutdown, and when it ended.
func EndOfBoot(tail string) (clean bool, ended time.Time) {
	for _, line := range strings.Split(tail, "\n") {
		if t, ok := parseJournalTime(line); ok {
			ended = t
			if cleanEnd.MatchString(line) {
				clean = true
			}
		}
	}
	return clean, ended
}

type BootReport struct {
	Boot    int
	Events  []Event
	Clean   bool
	Ended   time.Time
	HasLog  bool
	Checked bool // the end of the boot was looked at (only for finished boots)
}

func scanBoot(n int) BootReport {
	b := BootReport{Boot: n}
	// Kernel messages carry the memory, graphics, thermal and disk errors; systemd-oomd logs under its own name.
	k, kerr := journal("-b", fmt.Sprint(n), "-k")
	o, oerr := journal("-b", fmt.Sprint(n), "-t", "systemd-oomd")
	if kerr == nil || k != "" || oerr == nil || o != "" {
		b.HasLog = true
		b.Events = ScanEvents(k + "\n" + o)
		sort.Slice(b.Events, func(i, j int) bool { return b.Events[i].T.Before(b.Events[j].T) })
	}
	if n < 0 {
		tail, err := journal("-b", fmt.Sprint(n), "-n", "400")
		if err == nil || tail != "" {
			b.HasLog, b.Checked = true, true
			b.Clean, b.Ended = EndOfBoot(tail)
		}
	}
	return b
}

// Cause picks the likeliest reason a boot stopped without a normal shutdown:
// the last matching event within 5 minutes before the end, ordered by how
// directly it can freeze a machine.
func Cause(b BootReport) (kind string, ev Event, ok bool) {
	if !b.Checked || b.Clean || b.Ended.IsZero() {
		return "", Event{}, false
	}
	order := map[string]int{"panic": 0, "oom": 1, "gpu": 2, "thermal": 3, "disk": 4, "hung": 5}
	var best *Event
	for i := range b.Events {
		e := b.Events[i]
		if b.Ended.Sub(e.T) > 5*time.Minute || e.T.After(b.Ended) {
			continue
		}
		if best == nil || order[e.Kind] < order[best.Kind] || (order[e.Kind] == order[best.Kind] && e.T.After(best.T)) {
			best = &e
		}
	}
	if best == nil {
		return "", Event{}, false
	}
	return best.Kind, *best, true
}

func kindTitle(id string) string {
	for _, k := range kinds {
		if k.id == id {
			return L(k.title[0], k.title[1])
		}
	}
	return id
}

// FrozeLines renders the report. last is kerna's own history just before the end, if any.
func FrozeLines(p palette, boots []BootReport, last []Entry) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("why did it freeze", "neden dondu")), ""}
	any := false
	for _, b := range boots {
		if !b.HasLog {
			continue
		}
		any = true
		name := L("The previous session", "Önceki oturum")
		if b.Boot == 0 {
			name = L("This session", "Bu oturum")
		}
		switch {
		case b.Checked && !b.Clean:
			out = append(out, "  "+p.c(cBad, "●")+" "+p.c(cBold, name+": "+L("it ended without a normal shutdown", "normal kapanış olmadan bitti"))+p.c(cDim, "  "+b.Ended.Format("Mon 02 Jan 15:04:05")))
			out = append(out, p.c(cDim, "    "+L("A freeze, a power loss or a forced power-off all look like this in the log.", "Donma, elektrik kesintisi ve zorla kapatma günlükte böyle görünür.")))
		case b.Checked:
			out = append(out, "  "+p.c(cOK, "●")+" "+p.c(cBold, name+": "+L("it ended with a normal shutdown", "normal kapanışla bitti"))+p.c(cDim, "  "+b.Ended.Format("Mon 02 Jan 15:04:05")))
		default:
			out = append(out, "  "+p.c(cDim, "●")+" "+p.c(cBold, name))
		}
		if kind, ev, ok := Cause(b); ok {
			out = append(out, "    "+p.c(cBold+";"+cWarn, L("Most likely: ", "En olası: "))+kindTitle(kind)+p.c(cDim, "  "+ev.T.Format("15:04:05")))
			out = append(out, p.c(cDim, "      "+ev.Line))
		} else if b.Checked && !b.Clean {
			out = append(out, "    "+L("Nothing in the log points at a cause in the last 5 minutes. That fits a hard freeze or a power loss, which leave no trace.", "Günlükte son 5 dakikada bir sebebe işaret eden bir şey yok. Bu, iz bırakmayan bir sert donmaya ya da elektrik kesintisine uyar."))
		}
		if len(b.Events) > 0 {
			out = append(out, p.c(cDim, "    "+L("Events in this log:", "Bu günlükteki olaylar:")))
			shown := b.Events
			if len(shown) > 12 {
				shown = shown[len(shown)-12:]
			}
			for _, e := range shown {
				out = append(out, "      "+p.c(cDim, e.T.Format("15:04:05")+"  ")+kindTitle(e.Kind))
			}
		} else {
			out = append(out, p.c(cDim, "    "+L("No memory, graphics, thermal or disk errors in this log.", "Bu günlükte bellek, ekran kartı, ısınma ya da disk hatası yok.")))
		}
		out = append(out, "")
	}
	if !any {
		return append(out, "  "+L("The system log could not be read. Is journalctl allowed for your user (groups: wheel, adm or systemd-journal)?", "Sistem günlüğü okunamadı. Kullanıcın journalctl okuyabiliyor mu (gruplar: wheel, adm ya da systemd-journal)?"))
	}
	unclean := false
	for _, b := range boots {
		if b.Checked && !b.Clean {
			unclean = true
		}
	}
	if !unclean {
		return out
	}
	if len(last) > 0 {
		out = append(out, p.c(cDim, "  "+L("What kerna saw in the last minutes before it ended:", "kerna'nın bitişten önceki son dakikalarda gördüğü:")))
		sort.Slice(last, func(i, j int) bool { return last[i].T.Before(last[j].T) })
		for _, e := range last[max(0, len(last)-4):] {
			line := fmt.Sprintf("    %s  CPU %.0f%%  %s cpu %.0f%% disk %.0f%% %s %.0f%%", e.T.Format("15:04:05"), e.CPU, L("stalled:", "bekleme:"), e.PCPU, e.PIO, L("memory", "bellek"), e.PMem)
			if e.Temp > 0 {
				line += fmt.Sprintf("  %.0f °C", e.Temp)
			}
			out = append(out, line)
			for _, f := range e.Findings {
				out = append(out, p.c(cDim, "      · "+titleOf(f.ID)))
			}
		}
		out = append(out, "")
	} else {
		out = append(out, p.c(cDim, "  "+L("kerna has no history for that time. Install the recorder to see the last minutes next time: kerna service install", "kerna'nın o zamana ait geçmişi yok. Bir dahaki sefere son dakikaları görmek için kaydı kur: kerna service install")), "")
	}
	return out
}
