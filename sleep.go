package main

import (
	"fmt"
	"strings"
	"time"
)

// Sleep and suspend: the kernel's own counters plus the battery drop kerna
// recorded while no samples were written (asleep or powered off).

type SleepStats struct {
	Success, Fail                 int64
	LastFailedDev, LastFailedStep string
	MemSleep                      string // e.g. "s2idle [deep]"
	Known                         bool
}

func (s Source) readSleep() SleepStats {
	var st SleepStats
	ok := false
	if n, found := s.readInt("sys/power/suspend_stats/success"); found {
		st.Success, ok = n, true
	}
	st.Fail, _ = s.readInt("sys/power/suspend_stats/fail")
	st.LastFailedDev, _ = s.read("sys/power/suspend_stats/last_failed_dev")
	st.LastFailedStep, _ = s.read("sys/power/suspend_stats/last_failed_step")
	st.MemSleep, _ = s.read("sys/power/mem_sleep")
	st.Known = ok || st.MemSleep != ""
	return st
}

// selectedSleep returns the mode in [brackets] and whether "deep" is on offer.
func selectedSleep(memSleep string) (selected string, deepAvailable bool) {
	for _, f := range strings.Fields(memSleep) {
		name := strings.Trim(f, "[]")
		if name == "deep" {
			deepAvailable = true
		}
		if strings.HasPrefix(f, "[") {
			selected = name
		}
	}
	return
}

// Gap is a stretch of time with no recorded samples between two unplugged ones.
type Gap struct {
	From, To   time.Time
	From0, To0 int // battery percent at the start and the end
}

func (g Gap) Hours() float64 { return g.To.Sub(g.From).Hours() }
func (g Gap) Rate() float64  { return float64(g.From0-g.To0) / g.Hours() } // percent per hour

// SleepGaps finds the long silences of the recorder while on battery.
func SleepGaps(es []Entry) []Gap {
	var out []Gap
	for i := 1; i < len(es); i++ {
		a, b := es[i-1], es[i]
		if b.T.Sub(a.T) < 20*time.Minute || a.Bat == 0 || b.Bat == 0 || a.Plugged || b.Plugged {
			continue
		}
		out = append(out, Gap{From: a.T, To: b.T, From0: a.Bat, To0: b.Bat})
	}
	return out
}

const sleepDrainWarn = 1.5 // percent per hour; deep sleep is usually well under 1

func SleepLines(p palette, st SleepStats, es []Entry) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("sleep", "uyku")), ""}
	if !st.Known && len(es) == 0 {
		return append(out, "  "+L("The kernel reports nothing about sleep on this machine, and there is no history.", "Çekirdek bu makinede uyku hakkında bir şey bildirmiyor ve geçmiş de yok."))
	}
	if st.Known {
		out = append(out, "  "+L(fmt.Sprintf("Suspends since boot: %d succeeded, %d failed.", st.Success, st.Fail), fmt.Sprintf("Açılıştan beri uykuya geçişler: %d başarılı, %d başarısız.", st.Success, st.Fail)))
		if st.Fail > 0 {
			out = append(out, "  "+p.c(cWarn, "● ")+L("The last failure: device "+st.LastFailedDev+", step "+st.LastFailedStep+". That device stopped the laptop from sleeping.", "Son başarısızlık: cihaz "+st.LastFailedDev+", adım "+st.LastFailedStep+". Bu cihaz dizüstünün uyumasını engelledi."))
			out = append(out, p.c(cDim, "    "+L("Find the cause in the log:", "Sebebi günlükte bul:")+" journalctl -k -g 'PM:' -b"))
		}
		if sel, deep := selectedSleep(st.MemSleep); sel == "s2idle" && deep {
			out = append(out, "  "+p.c(cAccent, "● ")+L("Sleep mode is s2idle although \"deep\" is available. Deep sleep usually drains far less battery.", "Uyku modu s2idle, oysa \"deep\" mevcut. Derin uyku genelde çok daha az pil harcar."))
			out = append(out, p.c(cDim, "    "+L("Try the kernel parameter: mem_sleep_default=deep (some laptops sleep badly in deep mode, so test it)", "Çekirdek parametresini dene: mem_sleep_default=deep (bazı dizüstüler deep modda kötü uyur, bu yüzden test et)")))
		} else if sel != "" {
			out = append(out, p.c(cDim, "  "+L("Sleep mode: "+sel, "Uyku modu: "+sel)))
		}
		out = append(out, "")
	}
	gaps := SleepGaps(es)
	if len(gaps) == 0 {
		out = append(out, p.c(cDim, "  "+L("No long silences in the history while on battery (or no history yet: kerna service install).", "Pilde geçmişte uzun sessizlik yok (ya da henüz geçmiş yok: kerna service install).")))
		return out
	}
	out = append(out, "  "+p.c(cBold, L("While kerna was not recording (asleep or off), on battery:", "kerna kayıt yapmazken (uykuda ya da kapalı), pilde:")))
	for _, g := range gaps {
		line := fmt.Sprintf("    %s–%s  %s h   %d%% → %d%%   %s%%/h", g.From.Format("Mon 15:04"), g.To.Format("15:04"), fl(g.Hours(), 1), g.From0, g.To0, fl(g.Rate(), 1))
		if g.Rate() >= sleepDrainWarn {
			line = p.c(cWarn, line) + p.c(cWarn, "  "+L("high", "yüksek"))
		}
		out = append(out, line)
	}
	out = append(out, p.c(cDim, "  "+L("A laptop that is really off drains almost nothing, so a high rate means it was asleep and something kept it busy.", "Gerçekten kapalı bir dizüstü neredeyse hiç pil harcamaz; yüksek oran uykuda olduğunu ve bir şeyin onu meşgul ettiğini gösterir.")))
	return out
}
