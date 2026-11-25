package main

import (
	"fmt"
	"sort"
	"strings"
)

type Severity int

const (
	Info Severity = iota
	Warn
	Bad
)

// Finding is one diagnosed cause: what is wrong, why, the evidence behind it
// and a command the user may run. cause itself never runs the hint.
type Finding struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Sev      Severity `json:"-"`
	Title    string   `json:"title"`
	Why      string   `json:"why"`
	Evidence []string `json:"evidence,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

func (s Severity) String() string { return [...]string{"info", "warning", "problem"}[s] }

// protected names are parts of the desktop or the system. cause never
// suggests pausing them.
var protected = map[string]bool{
	"Hyprland": true, "sway": true, "Xorg": true, "Xwayland": true, "gnome-shell": true,
	"kwin_wayland": true, "kwin_x11": true, "plasmashell": true, "systemd": true, "sshd": true,
	"login": true, "qs": true, "waybar": true, "pipewire": true, "wireplumber": true, "dbus-daemon": true,
}

// Thresholds. PSI numbers are the percent of the last 10 seconds in which
// tasks were stalled, which is what a user perceives as "slow".
const (
	psiWarn      = 10.0
	psiBad       = 30.0
	hotC         = 88.0
	throttleFreq = 0.70
	drainWatts   = 15.0
)

func human(b float64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB/s", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB/s", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KiB/s", b/(1<<10))
	}
	return fmt.Sprintf("%.0f B/s", b)
}

func mib(kib int64) string {
	if kib >= 1<<20 {
		return fmt.Sprintf("%.1f GiB", float64(kib)/(1<<20))
	}
	return fmt.Sprintf("%d MiB", kib/1024)
}

func names(rs []Rate, f func(Rate) string) []string {
	var out []string
	for _, r := range rs {
		out = append(out, fmt.Sprintf("%s (pid %d) %s", r.Comm, r.PID, f(r)))
	}
	return out
}

func coreStr(x Rate) string {
	return L(fmt.Sprintf("%.0f%% of one core", x.CPU), fmt.Sprintf("bir çekirdeğin %%%.0f'i", x.CPU))
}

// hint joins a command and a translated comment.
func hint(cmd, en, tr string) string { return cmd + "   # " + L(en, tr) }

func sevOf(v float64) Severity {
	if v >= psiBad {
		return Bad
	}
	return Warn
}

// Diagnose applies every rule and returns the findings, worst first.
func Diagnose(r Report) []Finding {
	var out []Finding
	st := r.Static

	// 1. CPU is the bottleneck: tasks are waiting for a core.
	if p, ok := r.PSI["cpu"]; ok && p.SomeAvg10 >= psiWarn {
		groups := r.GroupCPU(3)
		f := Finding{ID: "cpu-pressure", Sev: sevOf(p.SomeAvg10),
			Title: L("Programs are waiting for the CPU", "Programlar işlemciyi bekliyor"),
			Why: L(fmt.Sprintf("For %.0f%% of the last 10 seconds at least one task was ready to run but had no free core. This is what makes the system feel slow.", p.SomeAvg10),
				fmt.Sprintf("Son 10 saniyenin %%%.0f'inde en az bir görev çalışmaya hazırdı ama boş çekirdek yoktu. Sistemin yavaş hissettirmesinin sebebi bu.", p.SomeAvg10)),
		}
		for _, g := range groups {
			if g.CPU < 5 {
				continue
			}
			who := fmt.Sprintf("%s (pid %d)", g.Comm, g.PID)
			if g.Count > 1 {
				who = fmt.Sprintf("%s ×%d", g.Comm, g.Count)
			}
			f.Evidence = append(f.Evidence, fmt.Sprintf("%s %s", who, L(fmt.Sprintf("%.0f%% of one core", g.CPU), fmt.Sprintf("bir çekirdeğin %%%.0f'i", g.CPU))))
		}
		if len(groups) > 0 && groups[0].CPU >= 50 {
			g := groups[0]
			switch {
			case protected[g.Comm]:
				f.Hint = L(fmt.Sprintf("%s is part of your desktop session: do not pause it. Look at what it is drawing or serving, for example a heavy window or animation.", g.Comm),
					fmt.Sprintf("%s masaüstü oturumunun parçası: durdurma. Neyi çizdiğine ya da neye hizmet ettiğine bak, örneğin ağır bir pencere ya da animasyon.", g.Comm))
			case g.Count > 1:
				f.Hint = hint("pkill -STOP -x "+g.Comm, "pause them to confirm; pkill -CONT -x "+g.Comm+" resumes", "doğrulamak için durdur; pkill -CONT -x "+g.Comm+" devam ettirir")
			default:
				f.Hint = hint(fmt.Sprintf("kill -STOP %d", g.PID), fmt.Sprintf("pause it to confirm it is the cause (kill -CONT %d resumes)", g.PID), fmt.Sprintf("sebep olduğunu doğrulamak için durdur (kill -CONT %d devam ettirir)", g.PID))
			}
		}
		out = append(out, f)
	}

	// 2. Disk is the bottleneck.
	if p, ok := r.PSI["io"]; ok && p.SomeAvg10 >= psiWarn {
		top := r.TopIO(3)
		f := Finding{ID: "io-pressure", Sev: sevOf(p.SomeAvg10),
			Title: L("Programs are waiting for the disk", "Programlar diski bekliyor"),
			Why: L(fmt.Sprintf("For %.0f%% of the last 10 seconds tasks were blocked on disk reads or writes.", p.SomeAvg10),
				fmt.Sprintf("Son 10 saniyenin %%%.0f'inde görevler disk okuma/yazma işlemlerinde bekledi.", p.SomeAvg10)),
		}
		for _, t := range top {
			if t.ReadBps+t.WriteBps > 256<<10 {
				f.Evidence = append(f.Evidence, L(fmt.Sprintf("%s (pid %d) read %s, write %s", t.Comm, t.PID, human(t.ReadBps), human(t.WriteBps)), fmt.Sprintf("%s (pid %d) okuma %s, yazma %s", t.Comm, t.PID, human(t.ReadBps), human(t.WriteBps))))
			}
		}
		if len(f.Evidence) == 0 {
			f.Evidence = []string{L("no process you own is the main source (it may be another user, the kernel or a btrfs/zram background job)", "baş sorumlu sana ait bir süreç değil (başka bir kullanıcı, çekirdek ya da btrfs/zram arka plan işi olabilir)")}
		}
		f.Hint = hint("sudo iotop -oPa", "per-process disk use for every user (needs the iotop package)", "her kullanıcı için süreç başına disk kullanımı (iotop paketi gerekir)")
		out = append(out, f)
	}

	// 3. Memory pressure and swap thrashing.
	availPct := 0.0
	if st.MemTotalKiB > 0 {
		availPct = float64(st.MemAvailKiB) / float64(st.MemTotalKiB) * 100
	}
	memP := r.PSI["memory"]
	if memP.SomeAvg10 >= 5 || (availPct > 0 && availPct < 8) || r.SwapPerS > 200 {
		sev := Warn
		if memP.SomeAvg10 >= psiBad || r.SwapPerS > 1000 || availPct < 4 {
			sev = Bad
		}
		f := Finding{ID: "memory-pressure", Sev: sev,
			Title: L("Memory is running out", "Bellek tükeniyor"),
			Why: L(fmt.Sprintf("Only %.0f%% of RAM is available and the system spent %.0f%% of the last 10 s stalled on memory. It is moving memory to and from swap instead of working.", availPct, memP.SomeAvg10),
				fmt.Sprintf("RAM'in yalnızca %%%.0f'ı kullanılabilir ve sistem son 10 saniyenin %%%.0f'ini bellek yüzünden bekleyerek geçirdi. Çalışmak yerine belleği takasa (swap) taşıyor.", availPct, memP.SomeAvg10)),
			Evidence: names(r.TopMem(3), func(x Rate) string { return mib(x.RSSKiB) }),
		}
		if r.SwapPerS > 0 {
			f.Evidence = append(f.Evidence, L(fmt.Sprintf("swap traffic: %.0f pages/s", r.SwapPerS), fmt.Sprintf("takas trafiği: saniyede %.0f sayfa", r.SwapPerS)))
		}
		f.Hint = hint("ps -eo pid,rss,comm --sort=-rss | head", "then close or restart the biggest one", "sonra en büyüğünü kapat ya da yeniden başlat")
		out = append(out, f)
	}

	// 4. Thermal throttling: hot and the clocks are well below maximum.
	if st.MaxTempC >= hotC && st.FreqRatio > 0 && st.FreqRatio < throttleFreq && r.CPUBusy > 0.5 {
		f := Finding{ID: "thermal-throttle", Sev: Bad,
			Title: L("The CPU is slowing itself down because it is hot", "İşlemci ısındığı için kendini yavaşlatıyor"),
			Why: L(fmt.Sprintf("%.0f °C on %s while the cores run at only %.0f%% of their maximum speed under load.", st.MaxTempC, st.MaxTempZone, st.FreqRatio*100),
				fmt.Sprintf("%s üzerinde %.0f °C ve çekirdekler yük altında azami hızlarının yalnızca %%%.0f'inde çalışıyor.", st.MaxTempZone, st.MaxTempC, st.FreqRatio*100)),
			Evidence: names(r.TopCPU(2), coreStr),
			Hint:     hint("powerprofilesctl set balanced", "or quiet; also check that the vents and fans are free", "ya da quiet; ayrıca havalandırma deliklerinin ve fanın açık olduğunu kontrol et"),
		}
		out = append(out, f)
	}

	// 5. dGPU awake. Only meaningful if there is an integrated GPU that drives the screen.
	var dgpu *GPU
	hasOther := false
	for i, g := range st.GPUs {
		if g.Vendor == "nvidia" && !g.Boot {
			dgpu = &st.GPUs[i]
		} else {
			hasOther = true
		}
	}
	onBattery := st.Battery != nil && strings.EqualFold(st.Battery.Status, "Discharging")
	if dgpu != nil && hasOther && dgpu.Runtime == "active" {
		var holders []string
		for _, p := range r.Procs {
			if p.HoldsGPU {
				holders = append(holders, fmt.Sprintf("%s (pid %d)", p.Comm, p.PID))
			}
		}
		sort.Strings(holders)
		sev := Info
		if onBattery {
			sev = Warn
		}
		f := Finding{ID: "dgpu-awake", Sev: sev,
			Title: L("The NVIDIA GPU is awake", "NVIDIA ekran kartı uyanık"),
			Why: L("The discrete GPU is powered on. It draws power even when idle, which costs battery and adds heat and fan noise.",
				"Ayrık ekran kartı açık. Boştayken bile güç çeker, bu da pil ömrünü kısaltır, ısıyı ve fan gürültüsünü artırır."),
		}
		switch {
		case dgpu.Control == "on":
			f.Why = L("The discrete GPU is powered on and its runtime power management is set to \"on\", so the kernel is not allowed to switch it off, even when no program needs it.",
				"Ayrık ekran kartı açık ve çalışma zamanı güç yönetimi \"on\" durumunda; yani hiçbir program ihtiyaç duymasa bile çekirdeğin onu kapatmasına izin yok.")
			f.Evidence = append(f.Evidence, fmt.Sprintf("/sys/bus/pci/devices/%s/power/control = on", dgpu.Addr))
			f.Hint = hint(fmt.Sprintf("echo auto | sudo tee /sys/bus/pci/devices/%s/power/control", dgpu.Addr), "until reboot. The NVIDIA driver also needs NVreg_DynamicPowerManagement=0x02 for the GPU to actually sleep", "yeniden başlatmaya kadar geçerli. GPU'nun gerçekten uyuması için NVIDIA sürücüsüne ayrıca NVreg_DynamicPowerManagement=0x02 gerekir")
		case len(holders) > 0:
			f.Evidence = append(f.Evidence, L("kept awake by: ", "uyanık tutan: ")+strings.Join(holders, ", "))
			f.Hint = L("close or restart the programs above, or run them on the integrated GPU; a compositor that renders on this GPU keeps it awake by design",
				"yukarıdaki programları kapat ya da tümleşik ekran kartında çalıştır; bu kartta çizim yapan bir pencere yöneticisi kartı tasarım gereği uyanık tutar")
		default:
			f.Evidence = append(f.Evidence, L("no process you own has it open (a root service, another user or a browser sandbox may)", "açık tutan sana ait bir süreç yok (bir root servisi, başka bir kullanıcı ya da tarayıcı korumalı alanı olabilir)"))
			f.Hint = hint("sudo fuser -v /dev/nvidia*", "shows every holder, including other users", "başka kullanıcılar dahil açık tutan herkesi gösterir")
		}
		if len(holders) > 0 && dgpu.Control == "on" {
			f.Evidence = append(f.Evidence, L("also open in: ", "ayrıca açık olan: ")+strings.Join(holders, ", "))
		}
		if !onBattery && st.Battery != nil {
			f.Title = L("The NVIDIA GPU is awake (plugged in, so no battery cost)", "NVIDIA ekran kartı uyanık (fişte, pile maliyeti yok)")
		}
		out = append(out, f)
	}

	// 6. Battery drains fast.
	if b := st.Battery; b != nil && onBattery && b.Watts >= drainWatts {
		f := Finding{ID: "battery-drain", Sev: Warn,
			Title: L("The battery is draining fast", "Pil hızlı tükeniyor"),
			Why: L(fmt.Sprintf("The system draws %.1f W on battery, which lasts about %.1f hours from now. A quiet laptop is usually 5 to 10 W.", b.Watts, b.HoursLeft),
				fmt.Sprintf("Sistem pilde %.1f W çekiyor, bu hızla yaklaşık %.1f saat dayanır. Sakin bir dizüstü genelde 5 ila 10 W çeker.", b.Watts, b.HoursLeft)),
			Evidence: names(r.TopCPU(3), coreStr),
		}
		if dgpu != nil && dgpu.Runtime == "active" {
			f.Evidence = append(f.Evidence, L("the NVIDIA GPU is awake (see above)", "NVIDIA ekran kartı uyanık (yukarıya bak)"))
		}
		f.Hint = "powerprofilesctl set power-saver"
		out = append(out, f)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Sev > out[j].Sev })
	for i := range out {
		out[i].Severity = out[i].Sev.String()
	}
	return out
}
