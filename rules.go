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
// and a command the user may run. kerna itself never runs the hint.
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
// They are variables so the config file can change them.
var (
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

func gpuName(v string) string {
	switch v {
	case "nvidia":
		return "NVIDIA"
	case "amd":
		return "AMD"
	case "intel":
		return "Intel"
	}
	return "GPU " + v
}

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
			who := label(g.Comm, g.PID, g.Count)
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

	// 5. A discrete GPU is awake. Only meaningful if another GPU drives the screen.
	var dgpu *GPU
	hasBoot := false
	for i, g := range st.GPUs {
		if g.Boot {
			hasBoot = true
		}
		if !g.Boot && dgpu == nil {
			dgpu = &st.GPUs[i]
		}
	}
	if !hasBoot {
		dgpu = nil
	}
	onBattery := st.Battery != nil && strings.EqualFold(st.Battery.Status, "Discharging")
	if dgpu != nil && dgpu.Runtime == "active" {
		name := gpuName(dgpu.Vendor)
		var holders []string
		if dgpu.Vendor == "nvidia" { // only NVIDIA has its own device file; render nodes are shared
			for _, p := range r.Procs {
				if p.HoldsGPU {
					holders = append(holders, fmt.Sprintf("%s (pid %d)", p.Comm, p.PID))
				}
			}
			sort.Strings(holders)
		}
		sev := Info
		if onBattery {
			sev = Warn
		}
		f := Finding{ID: "dgpu-awake", Sev: sev,
			Title: L("The "+name+" GPU is awake", name+" ekran kartı uyanık"),
			Why: L("The discrete GPU is powered on. It draws power even when idle, which costs battery and adds heat and fan noise.",
				"Ayrık ekran kartı açık. Boştayken bile güç çeker, bu da pil ömrünü kısaltır, ısıyı ve fan gürültüsünü artırır."),
		}
		d3off := dgpu.Vendor == "nvidia" && strings.Contains(strings.ToLower(dgpu.NvidiaD3), "disabled")
		if d3off {
			f.Evidence = append(f.Evidence, L("NVIDIA driver: Runtime D3 (the sleep state) is "+strings.ToLower(dgpu.NvidiaD3), "NVIDIA sürücüsü: Runtime D3 (uyku durumu) "+trD3(dgpu.NvidiaD3)))
		}
		switch {
		case d3off:
			f.Why = L("The discrete GPU is powered on, and the NVIDIA driver does not allow it to sleep: its runtime power management (Runtime D3) is disabled. Two switches decide this, the driver option and the kernel's power/control file.",
				"Ayrık ekran kartı açık ve NVIDIA sürücüsü onun uyumasına izin vermiyor: çalışma zamanı güç yönetimi (Runtime D3) kapalı. Bunu iki anahtar belirliyor: sürücü seçeneği ve çekirdeğin power/control dosyası.")
			if dgpu.Control == "on" {
				f.Evidence = append(f.Evidence, fmt.Sprintf("/sys/bus/pci/devices/%s/power/control = on", dgpu.Addr))
			}
			f.Hint = "echo 'options nvidia NVreg_DynamicPowerManagement=0x02' | sudo tee /etc/modprobe.d/nvidia-pm.conf   # " +
				L("then reboot (run sudo mkinitcpio -P first if nvidia is in your initramfs); also set power/control to auto, with a udev rule or: echo auto | sudo tee /sys/bus/pci/devices/"+dgpu.Addr+"/power/control",
					"sonra yeniden başlat (nvidia initramfs içindeyse önce sudo mkinitcpio -P); ayrıca power/control değerini auto yap: udev kuralıyla ya da echo auto | sudo tee /sys/bus/pci/devices/"+dgpu.Addr+"/power/control")
		case dgpu.Control == "on":
			f.Why = L("The discrete GPU is powered on and its runtime power management is set to \"on\", so the kernel is not allowed to switch it off, even when no program needs it.",
				"Ayrık ekran kartı açık ve çalışma zamanı güç yönetimi \"on\" durumunda; yani hiçbir program ihtiyaç duymasa bile çekirdeğin onu kapatmasına izin yok.")
			f.Evidence = append(f.Evidence, fmt.Sprintf("/sys/bus/pci/devices/%s/power/control = on", dgpu.Addr))
			extra := ""
			if dgpu.Vendor == "nvidia" && dgpu.NvidiaD3 == "" {
				extra = L(" If it still does not sleep, the NVIDIA driver may also need NVreg_DynamicPowerManagement=0x02.", " Hâlâ uyumazsa NVIDIA sürücüsüne ayrıca NVreg_DynamicPowerManagement=0x02 gerekebilir.")
			}
			f.Hint = fmt.Sprintf("echo auto | sudo tee /sys/bus/pci/devices/%s/power/control   # %s", dgpu.Addr, L("until reboot."+extra, "yeniden başlatmaya kadar geçerli."+extra))
		case len(holders) > 0:
			f.Evidence = append(f.Evidence, L("kept awake by: ", "uyanık tutan: ")+strings.Join(holders, ", "))
			f.Hint = L("close or restart the programs above, or run them on the integrated GPU",
				"yukarıdaki programları kapat ya da tümleşik ekran kartında çalıştır")
		case dgpu.Vendor == "nvidia":
			f.Evidence = append(f.Evidence, L("no process you own has it open (a root service, another user or a browser sandbox may)", "açık tutan sana ait bir süreç yok (bir root servisi, başka bir kullanıcı ya da tarayıcı korumalı alanı olabilir)"))
			f.Hint = hint("sudo fuser -v /dev/nvidia*", "shows every holder, including other users", "başka kullanıcılar dahil açık tutan herkesi gösterir")
		default:
			f.Evidence = append(f.Evidence, L("runtime power management is on (auto), so something is using the GPU right now", "çalışma zamanı güç yönetimi açık (auto); yani şu an bir şey kartı kullanıyor"))
			f.Hint = hint("cat /sys/class/drm/card*/device/gpu_busy_percent", "how busy each GPU is (amdgpu)", "her kartın ne kadar meşgul olduğu (amdgpu)")
		}
		for _, h := range holders {
			switch {
			case strings.HasPrefix(h, "Hyprland "):
				f.Evidence = append(f.Evidence, L("Hyprland opens every GPU by default; AQ_DRM_DEVICES=/dev/dri/cardN limits it to one (it also disables outputs wired to the other GPU)", "Hyprland varsayılan olarak her ekran kartını açar; AQ_DRM_DEVICES=/dev/dri/cardN onu tek karta sınırlar (diğer karta bağlı çıkışları da kapatır)"))
			case strings.HasPrefix(h, "sway "):
				f.Evidence = append(f.Evidence, L("sway opens every GPU by default; WLR_DRM_DEVICES limits it to one", "sway varsayılan olarak her ekran kartını açar; WLR_DRM_DEVICES onu tek karta sınırlar"))
			}
		}
		if len(holders) > 0 && (dgpu.Control == "on" || d3off) {
			f.Evidence = append(f.Evidence, L("also open in: ", "ayrıca açık olan: ")+strings.Join(holders, ", "))
		}
		if !onBattery && st.Battery != nil {
			f.Title = L("The "+name+" GPU is awake (plugged in, so no battery cost)", name+" ekran kartı uyanık (fişte, pile maliyeti yok)")
		}
		out = append(out, f)
	}

	// 5b. A GPU that is almost fully busy (amdgpu reports this).
	for _, g := range st.GPUs {
		if g.BusyPct >= 95 {
			sev := Info
			if onBattery {
				sev = Warn
			}
			out = append(out, Finding{ID: "gpu-busy", Sev: sev,
				Title: L(fmt.Sprintf("The %s GPU is %d%% busy", gpuName(g.Vendor), g.BusyPct), fmt.Sprintf("%s ekran kartı %%%d meşgul", gpuName(g.Vendor), g.BusyPct)),
				Why: L("A game, video or heavy window is using almost all of the GPU. That is normal while it runs, and it is why the fans and the battery work harder.",
					"Bir oyun, video ya da ağır bir pencere ekran kartının neredeyse tamamını kullanıyor. Çalışırken normal, fanların ve pilin daha çok yorulmasının sebebi de bu."),
				Hint: hint("nvtop", "shows which program is using each GPU (nvtop package)", "hangi programın hangi ekran kartını kullandığını gösterir (nvtop paketi)")})
		}
	}

	// 6. Battery drains fast. The kernel does not tell us watts per program without root,
	// so the suspects are ranked by CPU work and wake-ups, and said to be exactly that.
	if b := st.Battery; b != nil && onBattery && b.Watts >= drainWatts {
		f := Finding{ID: "battery-drain", Sev: Warn,
			Title: L("The battery is draining fast", "Pil hızlı tükeniyor"),
			Why: L(fmt.Sprintf("The system draws %.1f W on battery, which lasts about %.1f hours from now. A quiet laptop is usually 5 to 10 W.", b.Watts, b.HoursLeft),
				fmt.Sprintf("Sistem pilde %s W çekiyor, bu hızla yaklaşık %s saat dayanır. Sakin bir dizüstü genelde 5 ila 10 W çeker.", fl(b.Watts, 1), fl(b.HoursLeft, 1))),
		}
		for _, g := range r.GroupCPU(3) {
			if g.CPU < 3 && g.Wakeups < 100 {
				continue
			}
			who := label(g.Comm, g.PID, g.Count)
			f.Evidence = append(f.Evidence, fmt.Sprintf("%s %s, %s", who,
				L(fmt.Sprintf("%.0f%% of one core", g.CPU), fmt.Sprintf("bir çekirdeğin %%%.0f'i", g.CPU)),
				L(fmt.Sprintf("%.0f wake-ups/s", g.Wakeups), fmt.Sprintf("saniyede %.0f uyanma", g.Wakeups))))
		}
		for _, g := range st.GPUs {
			if g.Watts >= 8 {
				f.Evidence = append(f.Evidence, L(fmt.Sprintf("the %s GPU alone draws %.1f W", gpuName(g.Vendor), g.Watts), fmt.Sprintf("yalnızca %s ekran kartı %s W çekiyor", gpuName(g.Vendor), fl(g.Watts, 1))))
			}
		}
		if dgpu != nil && dgpu.Runtime == "active" {
			f.Evidence = append(f.Evidence, L("the discrete GPU is awake (see above)", "ayrık ekran kartı uyanık (yukarıya bak)"))
		}
		f.Evidence = append(f.Evidence, L("ranked by CPU work and wake-ups: the kernel does not report watts per program without root", "CPU işi ve uyanma sayısına göre sıralı: çekirdek root olmadan program başına watt vermez"))
		f.Hint = "powerprofilesctl set power-saver"
		out = append(out, f)
	}

	// 8. The network is losing packets, or the Wi-Fi signal is weak.
	if r.TCPOut >= 300 && r.RetransPct >= 3 {
		sev := Warn
		if r.RetransPct >= 8 {
			sev = Bad
		}
		f := Finding{ID: "net-loss", Sev: sev,
			Title: L("Network connections are losing packets", "Ağ bağlantıları paket kaybediyor"),
			Why: L(fmt.Sprintf("%s%% of the TCP segments sent in the last seconds had to be sent again. Pages and downloads feel slow when that happens: usually a weak Wi-Fi signal, a congested line or a bad cable.", fl(r.RetransPct, 1)),
				fmt.Sprintf("Son saniyelerde gönderilen TCP parçalarının %%%s'i yeniden gönderilmek zorunda kaldı. Bu olunca sayfalar ve indirmeler yavaş hisseder: genelde zayıf Wi-Fi sinyali, tıkalı hat ya da kötü kablo.", fl(r.RetransPct, 1))),
			Evidence: []string{L(fmt.Sprintf("%d segments sent, %s%% sent again", r.TCPOut, fl(r.RetransPct, 1)), fmt.Sprintf("%d parça gönderildi, %%%s tekrar gönderildi", r.TCPOut, fl(r.RetransPct, 1)))},
		}
		if st.WifiIface != "" {
			f.Evidence = append(f.Evidence, L(fmt.Sprintf("Wi-Fi signal on %s: %d dBm", st.WifiIface, st.WifiDBm), fmt.Sprintf("%s üzerinde Wi-Fi sinyali: %d dBm", st.WifiIface, st.WifiDBm)))
		}
		f.Hint = hint("ping -c 20 1.1.1.1", "look at the packet loss and the spread of the times; try the same on a cable or closer to the router", "paket kaybına ve sürelerin dağılımına bak; aynısını kabloyla ya da modeme yakın dene")
		out = append(out, f)
	}
	if st.WifiIface != "" && st.WifiDBm <= -75 {
		sev := Warn
		if st.WifiDBm <= -82 {
			sev = Bad
		}
		out = append(out, Finding{ID: "wifi-weak", Sev: sev,
			Title: L("The Wi-Fi signal is weak", "Wi-Fi sinyali zayıf"),
			Why: L(fmt.Sprintf("The signal on %s is %d dBm. Below about -70 dBm speeds drop and connections get unreliable.", st.WifiIface, st.WifiDBm),
				fmt.Sprintf("%s üzerindeki sinyal %d dBm. Yaklaşık -70 dBm'in altında hız düşer ve bağlantılar güvenilmez olur.", st.WifiIface, st.WifiDBm)),
			Hint: hint("iw dev "+st.WifiIface+" link", "signal, speed and band of the current connection", "mevcut bağlantının sinyali, hızı ve bandı")})
	}

	// 7. A performance profile while running on battery.
	if onBattery && (st.Profile == "performance" || st.EPP == "performance") {
		sev := Info
		if st.Battery.Watts >= drainWatts {
			sev = Warn
		}
		what := st.Profile
		src := "/sys/firmware/acpi/platform_profile"
		if st.Profile != "performance" {
			what, src = st.EPP, "cpu0/cpufreq/energy_performance_preference"
		}
		out = append(out, Finding{ID: "power-profile", Sev: sev,
			Title: L("A performance power profile is active on battery", "Pilde performans güç profili açık"),
			Why: L("The system is told to favour speed over battery life, so clocks stay high and the battery drains faster.",
				"Sisteme pil ömrü yerine hızı tercih etmesi söylenmiş; saat hızları yüksek kalıyor ve pil daha hızlı tükeniyor."),
			Evidence: []string{fmt.Sprintf("%s = %s", src, what)},
			Hint:     hint("powerprofilesctl set balanced", "or power-saver for the longest battery life", "ya da en uzun pil ömrü için power-saver")})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Sev > out[j].Sev })
	for i := range out {
		out[i].Severity = out[i].Sev.String()
	}
	return out
}

// trD3 translates the driver's own wording of the Runtime D3 status.
func trD3(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "disabled by default"):
		return "varsayılan olarak kapalı"
	case strings.Contains(l, "disabled"):
		return "kapalı"
	case strings.Contains(l, "enabled"):
		return "açık"
	}
	return l
}
