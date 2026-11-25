package main

import "fmt"

// Short, plain descriptions of process names people often do not recognise.
// Only names whose meaning is not obvious are listed; unknown names are shown as they are.
var processInfo = map[string][2]string{
	"qs":              {"Quickshell, your desktop shell", "Quickshell, masaüstü kabuğun"},
	"quickshell":      {"Quickshell, your desktop shell", "Quickshell, masaüstü kabuğun"},
	"Hyprland":        {"your window manager", "pencere yöneticin"},
	"Xwayland":        {"runs X11 apps on Wayland", "X11 uygulamalarını Wayland'de çalıştırır"},
	"pipewire":        {"sound and video routing", "ses ve video yönlendirme"},
	"pipewire-pulse":  {"PulseAudio compatibility for PipeWire", "PipeWire için PulseAudio uyumluluğu"},
	"wireplumber":     {"PipeWire session manager", "PipeWire oturum yöneticisi"},
	"Isolated Web Co": {"Firefox web pages", "Firefox web sayfaları"},
	"Web Content":     {"Firefox web pages", "Firefox web sayfaları"},
	"RDD Process":     {"Firefox media decoder", "Firefox medya çözücü"},
	"Socket Process":  {"Firefox network", "Firefox ağ süreci"},
	"WebExtensions":   {"Firefox extensions", "Firefox eklentileri"},
	"cava":            {"audio visualizer", "ses görselleştirici"},
	"claude":          {"Claude Code", "Claude Code"},
	"gopls":           {"Go language server", "Go dil sunucusu"},
	"systemd-oomd":    {"kills programs when memory runs out", "bellek bitince programları kapatır"},
	"dbus-daemon":     {"message bus between programs", "programlar arası mesaj yolu"},
	"systemd-journal": {"collects the system log", "sistem günlüğünü toplar"},
	"kswapd0":         {"the kernel moving memory to swap", "çekirdeğin belleği takasa taşıması"},
	"kworker":         {"kernel background work", "çekirdek arka plan işi"},
}

func describe(comm string) string {
	if d, ok := processInfo[comm]; ok {
		return L(d[0], d[1])
	}
	return ""
}

func describeShort(comm string) string {
	if d := describe(comm); d != "" {
		return comm + " · " + d
	}
	return comm
}

// label names a program or a group of same-name programs, with what it is when known.
func label(comm string, pid, count int) string {
	s := comm
	if count > 1 {
		s += fmt.Sprintf(" ×%d", count)
	} else {
		s += fmt.Sprintf(" (pid %d)", pid)
	}
	if d := describe(comm); d != "" {
		s += " · " + d
	}
	return s
}
