package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// `kerna check` reports what kerna can and cannot see on this machine, so a
// missing finding has an explanation and a bug report has the facts. It reads
// the same files as everything else and contains no host name or user name.

type checkLine struct {
	ok   int // 1 works, 0 not available (normal), -1 problem
	what string
	note string
}

func (s Source) Check() []checkLine {
	var out []checkLine
	add := func(ok int, what, note string) { out = append(out, checkLine{ok, what, note}) }

	rel, _ := s.read("proc/sys/kernel/osrelease")
	add(1, L("Kernel", "Çekirdek"), rel)
	if b, err := os.ReadFile(s.p("etc/os-release")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				add(1, L("System", "Sistem"), strings.Trim(v, `"`))
			}
		}
	}
	add(1, "kerna", version+", "+L("language ", "dil ")+lang)

	// pressure
	var psi []string
	for _, k := range []string{"cpu", "io", "memory"} {
		if _, ok := s.read("proc/pressure", k); ok {
			psi = append(psi, k)
		}
	}
	if len(psi) == 3 {
		add(1, L("Pressure stall info (PSI)", "Baskı kaynaklı bekleme bilgisi (PSI)"), "cpu, io, memory")
	} else {
		add(-1, L("Pressure stall info (PSI)", "Baskı kaynaklı bekleme bilgisi (PSI)"), L("missing: the CPU, disk and memory findings need a kernel with PSI (boot with psi=1)", "yok: CPU, disk ve bellek bulguları PSI'lı bir çekirdek ister (psi=1 ile aç)"))
	}

	st := s.readStatic()
	add(map[bool]int{true: 1, false: -1}[st.CPUs > 0], L("CPUs", "İşlemci"), fmt.Sprint(st.CPUs))
	if st.FreqRatio > 0 {
		add(1, L("CPU clock speed", "CPU saat hızı"), L("readable", "okunabiliyor"))
	} else {
		add(0, L("CPU clock speed", "CPU saat hızı"), L("not reported: thermal throttling cannot be judged", "bildirilmiyor: ısınma yavaşlatması değerlendirilemez"))
	}
	if st.MaxTempC > 0 {
		add(1, L("Temperature", "Sıcaklık"), fmt.Sprintf("%s (%s)", fl(st.MaxTempC, 0)+" °C", st.MaxTempZone))
	} else {
		add(0, L("Temperature", "Sıcaklık"), L("no thermal zone readable", "okunabilir ısı bölgesi yok"))
	}

	if b := st.Battery; b != nil {
		add(1, L("Battery", "Pil"), fmt.Sprintf("%s, %s, %s", b.Name, strings.ToLower(b.Status), map[bool]string{true: fl(b.Watts, 1) + " W", false: L("no power reading", "güç okuması yok")}[b.Watts > 0]))
	} else {
		add(0, L("Battery", "Pil"), L("none (a desktop?)", "yok (masaüstü?)"))
	}
	if st.Profile != "" || st.EPP != "" {
		add(1, L("Power profile", "Güç profili"), strings.TrimSpace(st.Profile+" "+st.EPP))
	} else {
		add(0, L("Power profile", "Güç profili"), L("not reported", "bildirilmiyor"))
	}

	// GPUs
	if len(st.GPUs) == 0 {
		add(0, "GPU", L("no PCI display device found", "PCI ekran aygıtı bulunamadı"))
	}
	for _, g := range st.GPUs {
		var parts []string
		parts = append(parts, gpuName(g.Vendor))
		if g.Boot {
			parts = append(parts, L("drives the screen", "ekranı sürüyor"))
		}
		if g.Runtime != "" {
			parts = append(parts, g.Runtime)
		}
		if g.BusyPct >= 0 {
			parts = append(parts, L("busy % readable", "meşgul % okunabiliyor"))
		}
		if g.Watts > 0 {
			parts = append(parts, L("watts readable", "watt okunabiliyor"))
		}
		if g.NvidiaD3 != "" {
			parts = append(parts, "Runtime D3: "+g.NvidiaD3)
		}
		add(1, "GPU "+g.Addr, strings.Join(parts, ", "))
	}
	if idle := s.readGPUIdle(); len(idle) > 0 {
		add(1, L("Intel GPU idle residency", "Intel GPU boşta kalma süresi"), L("readable", "okunabiliyor"))
	}
	// DRM fdinfo for our own processes
	drmClients := 0
	if procs, err := os.ReadDir(s.p("proc")); err == nil {
		self := fmt.Sprint(os.Getpid())
		for _, e := range procs {
			if e.Name() != self {
				if ps, ok := s.snapProcQuick(e.Name()); ok && ps {
					drmClients++
				}
			}
		}
	}
	if drmClients > 0 {
		add(1, L("GPU time per program", "Program başına GPU süresi"), L(fmt.Sprintf("%d of your programs report it", drmClients), fmt.Sprintf("%d programın bildiriyor", drmClients)))
	} else {
		add(0, L("GPU time per program", "Program başına GPU süresi"), L("no program reports it (NVIDIA's proprietary driver does not)", "hiçbir program bildirmiyor (NVIDIA'nın kapalı kaynak sürücüsü bildirmez)"))
	}

	// network
	if _, ok := s.read("proc/net/snmp"); ok {
		add(1, L("TCP retransmits", "TCP yeniden gönderme"), "/proc/net/snmp")
	}
	if st.WifiIface != "" {
		add(1, L("Wi-Fi signal", "Wi-Fi sinyali"), fmt.Sprintf("%s %d dBm", st.WifiIface, st.WifiDBm))
	} else {
		add(0, L("Wi-Fi signal", "Wi-Fi sinyali"), L("the driver does not report it (the packet loss finding still works)", "sürücü bildirmiyor (paket kaybı bulgusu yine çalışır)"))
	}
	sl := s.readSleep()
	if sl.Known {
		add(1, L("Suspend counters", "Uyku sayaçları"), fmt.Sprintf("%d ok, %d failed, %s", sl.Success, sl.Fail, sl.MemSleep))
	} else {
		add(0, L("Suspend counters", "Uyku sayaçları"), L("not reported", "bildirilmiyor"))
	}

	// tools
	tool := func(name, what string) {
		if _, err := exec.LookPath(name); err == nil {
			add(1, name, what)
		} else {
			add(0, name, L("not installed: ", "kurulu değil: ")+what)
		}
	}
	tool("journalctl", L("needed for kerna why-froze", "kerna why-froze için gerekli"))
	tool("notify-send", L("needed for notifications", "bildirimler için gerekli"))
	if _, err := exec.LookPath("wl-copy"); err == nil {
		add(1, "wl-copy", L("the c key copies commands", "c tuşu komut kopyalar"))
	} else if _, err := exec.LookPath("xclip"); err == nil {
		add(1, "xclip", L("the c key copies commands", "c tuşu komut kopyalar"))
	} else {
		add(0, "wl-copy / xclip", L("not installed: the c key cannot copy", "kurulu değil: c tuşu kopyalayamaz"))
	}

	// history and config
	if es, err := readHistory(historyPath(), timeZero); err == nil {
		fi, _ := os.Stat(historyPath())
		add(1, L("History", "Geçmiş"), fmt.Sprintf("%d %s, %d KB", len(es), L("samples", "örnek"), fi.Size()/1024))
	} else {
		add(0, L("History", "Geçmiş"), L("not recording (kerna service install)", "kayıt yok (kerna service install)"))
	}
	c := LoadConfig()
	if len(c.Ignore) > 0 {
		add(1, L("Muted findings", "Susturulan bulgular"), strings.Join(c.Ignore, ", "))
	}
	if _, err := os.Stat(configPath()); err == nil {
		add(1, L("Config", "Ayar dosyası"), filepath.Base(filepath.Dir(configPath()))+"/"+filepath.Base(configPath()))
	}
	return out
}

func CheckLines(p palette, ls []checkLine) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("what kerna can see here", "kerna burada neyi görebiliyor")), ""}
	for _, l := range ls {
		mark, col := "✔", cOK
		switch l.ok {
		case 0:
			mark, col = "–", cDim
		case -1:
			mark, col = "✖", cBad
		}
		out = append(out, fmt.Sprintf("  %s %-34s %s", p.c(col, mark), l.what, p.c(cDim, l.note)))
	}
	out = append(out, "", p.c(cDim, "  "+L("Paste this into a bug report: it contains no host name, user name or file names.", "Bunu hata bildirimine yapıştır: ana makine adı, kullanıcı adı ya da dosya adı içermez.")))
	return out
}
