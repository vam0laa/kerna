package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { lang = "en" }

func TestParseStatLineWithSpacesAndParensInName(t *testing.T) {
	line := "4242 (Web Content (x)) S 1 4242 4242 0 -1 4194560 100 0 0 0 150 50 0 0 20 0 1 0 100 1000000 2500 18446744073709551615"
	p, ok := parseStatLine(line)
	if !ok || p.PID != 4242 || p.Comm != "Web Content (x)" {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
	if p.Ticks != 200 {
		t.Fatalf("ticks = %d, want 200", p.Ticks)
	}
	if want := int64(2500) * int64(os.Getpagesize()) / 1024; p.RSSKiB != want {
		t.Fatalf("rss = %d, want %d", p.RSSKiB, want)
	}
}

func TestParsePSI(t *testing.T) {
	p := parsePSI("some avg10=12.50 avg60=3.00 avg300=1.00 total=123\nfull avg10=4.25 avg60=1.00 avg300=0.50 total=45\n")
	if p.SomeAvg10 != 12.5 || p.FullAvg10 != 4.25 {
		t.Fatalf("got %+v", p)
	}
}

// fixture builds a fake /proc and /sys that the same readers parse.
type fixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *fixture { return &fixture{t: t, root: t.TempDir()} }

func (f *fixture) write(rel, content string) {
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) proc(pid int, comm string, ticks uint64, rssPages int, readB, writeB uint64) {
	f.write(fmt.Sprintf("proc/%d/stat", pid), fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 1 1 %d 0", pid, comm, ticks, rssPages))
	f.write(fmt.Sprintf("proc/%d/io", pid), fmt.Sprintf("rchar: 0\nwchar: 0\nread_bytes: %d\nwrite_bytes: %d\n", readB, writeB))
}

func (f *fixture) base(psiCPU, psiIO, psiMem string, availKiB int) {
	f.write("proc/stat", "cpu 100 0 100 800 0 0 0 0 0 0\n")
	f.write("proc/loadavg", "1.00 1.00 1.00 1/100 1\n")
	f.write("proc/meminfo", fmt.Sprintf("MemTotal: 16000000 kB\nMemAvailable: %d kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", availKiB))
	f.write("proc/vmstat", "pswpin 0\npswpout 0\npgmajfault 0\n")
	f.write("proc/pressure/cpu", "some avg10="+psiCPU+" avg60=0 avg300=0 total=0\n")
	f.write("proc/pressure/io", "some avg10="+psiIO+" avg60=0 avg300=0 total=0\nfull avg10=0 avg60=0 avg300=0 total=0\n")
	f.write("proc/pressure/memory", "some avg10="+psiMem+" avg60=0 avg300=0 total=0\nfull avg10=0 avg60=0 avg300=0 total=0\n")
	f.write("sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq", "4000000")
	f.write("sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq", "4000000")
}

func (f *fixture) diagnose(second func()) []Finding {
	src := Source{Root: f.root}
	a := src.readRaw()
	second()
	b := src.readRaw()
	b.At = a.At.Add(time.Second)
	return Diagnose(Diff(a, b, src.readStatic()))
}

func ids(fs []Finding) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.ID)
	}
	return strings.Join(s, ",")
}

func TestQuietSystemHasNoFindings(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.proc(10, "idle", 5, 100, 0, 0)
	if got := f.diagnose(func() { f.proc(10, "idle", 6, 100, 0, 0) }); len(got) != 0 {
		t.Fatalf("expected nothing, got %s", ids(got))
	}
}

func TestCPUPressureNamesTheHog(t *testing.T) {
	f := newFixture(t)
	f.base("45.00", "0.00", "0.00", 12000000)
	f.proc(10, "idle", 5, 100, 0, 0)
	f.proc(20, "ffmpeg", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(10, "idle", 5, 100, 0, 0); f.proc(20, "ffmpeg", 100, 100, 0, 0) })
	if len(got) != 1 || got[0].ID != "cpu-pressure" || got[0].Sev != Bad {
		t.Fatalf("got %s", ids(got))
	}
	if !strings.Contains(strings.Join(got[0].Evidence, "\n"), "ffmpeg (pid 20) 100% of one core") {
		t.Fatalf("evidence = %v", got[0].Evidence)
	}
	if !strings.Contains(got[0].Hint, "kill -STOP 20") {
		t.Fatalf("hint = %q", got[0].Hint)
	}
}

func TestIOPressureNamesTheWriter(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "35.00", "0.00", 12000000)
	f.proc(30, "rsync", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(30, "rsync", 1, 100, 0, 200<<20) })
	if len(got) != 1 || got[0].ID != "io-pressure" {
		t.Fatalf("got %s", ids(got))
	}
	if !strings.Contains(got[0].Evidence[0], "rsync") || !strings.Contains(got[0].Evidence[0], "200.0 MiB/s") {
		t.Fatalf("evidence = %v", got[0].Evidence)
	}
}

func TestMemoryPressure(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "20.00", 400000)
	f.proc(40, "leaky", 0, 3000000, 0, 0)
	got := f.diagnose(func() { f.proc(40, "leaky", 1, 3000000, 0, 0) })
	if len(got) != 1 || got[0].ID != "memory-pressure" {
		t.Fatalf("got %s", ids(got))
	}
}

func TestThermalThrottle(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("proc/stat", "cpu 100 0 100 800 0 0 0 0 0 0\n")
	f.write("sys/class/thermal/thermal_zone0/temp", "95000")
	f.write("sys/class/thermal/thermal_zone0/type", "x86_pkg_temp")
	f.write("sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq", "1600000")
	f.proc(50, "burn", 0, 100, 0, 0)
	got := f.diagnose(func() {
		f.write("proc/stat", "cpu 1000 0 1000 900 0 0 0 0 0 0\n")
		f.proc(50, "burn", 100, 100, 0, 0)
	})
	if ids(got) != "thermal-throttle" {
		t.Fatalf("got %s", ids(got))
	}
}

func TestDGPUControlOnIsTheRootCause(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/bus/pci/devices/0000:01:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:01:00.0/vendor", "0x10de")
	f.write("sys/bus/pci/devices/0000:01:00.0/power/runtime_status", "active")
	f.write("sys/bus/pci/devices/0000:01:00.0/power/control", "on")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:65:00.0/boot_vga", "1")
	f.write("sys/class/power_supply/BAT1/status", "Discharging")
	f.write("sys/class/power_supply/BAT1/capacity", "70")
	f.write("sys/class/power_supply/BAT1/power_now", "30000000")
	f.write("sys/class/power_supply/BAT1/energy_now", "47000000")
	f.proc(60, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(60, "idle", 1, 100, 0, 0) })
	if ids(got) != "dgpu-awake,battery-drain" {
		t.Fatalf("got %s", ids(got))
	}
	if !strings.Contains(got[0].Hint, "power/control") || !strings.Contains(got[0].Hint, "NVreg_DynamicPowerManagement") {
		t.Fatalf("hint = %q", got[0].Hint)
	}
	if !strings.Contains(got[1].Why, "30.0 W") || !strings.Contains(got[1].Why, "1.6 hours") {
		t.Fatalf("why = %q", got[1].Why)
	}
}

func TestDGPUAsleepIsFine(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/bus/pci/devices/0000:01:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:01:00.0/vendor", "0x10de")
	f.write("sys/bus/pci/devices/0000:01:00.0/power/runtime_status", "suspended")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.proc(60, "idle", 0, 100, 0, 0)
	if got := f.diagnose(func() { f.proc(60, "idle", 1, 100, 0, 0) }); len(got) != 0 {
		t.Fatalf("got %s", ids(got))
	}
}

func TestProcessThatRestartedIsIgnored(t *testing.T) {
	a := Raw{At: time.Now(), Procs: map[int]Proc{5: {PID: 5, Comm: "old", Ticks: 9000}}}
	b := Raw{At: a.At.Add(time.Second), Procs: map[int]Proc{5: {PID: 5, Comm: "new", Ticks: 10}}}
	if r := Diff(a, b, Static{}); len(r.Procs) != 0 {
		t.Fatalf("pid reuse must not produce a rate: %+v", r.Procs)
	}
}

func TestFrameIsPlainWithoutColor(t *testing.T) {
	r := Report{Window: 2 * time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50}}
	out := strings.Join(Frame(palette{false}, r, nil, &Hist{}, 80, "host", time.Now(), "", false), "\n")
	if strings.Contains(out, "\x1b[") || !strings.Contains(out, "Everything looks fine") {
		t.Fatalf("output = %q", out)
	}
}

func TestFrameShowsFindingAndHint(t *testing.T) {
	r := Report{Window: 2 * time.Second, PSI: map[string]PSI{"cpu": {SomeAvg10: 50}}}
	fs := []Finding{{ID: "cpu-pressure", Sev: Bad, Title: "Programs are waiting for the CPU", Why: "why", Evidence: []string{"x (pid 1) 90% of one core"}, Hint: "kill -STOP 1   # pause it"}}
	out := strings.Join(Frame(palette{false}, r, fs, &Hist{}, 80, "h", time.Now(), "", true), "\n")
	for _, want := range []string{"1 thing needs attention", "Programs are waiting for the CPU", "x (pid 1) 90% of one core", "$ kill -STOP 1", "pause it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestIncidentsGroupAndSplit(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 3, 0, 0, 0, time.Local)
	mk := func(min int, f bool, pc float64) Entry {
		e := Entry{T: t0.Add(time.Duration(min) * recordEvery), PCPU: pc}
		if f {
			e.Findings = []HFinding{{ID: "cpu-pressure", Sev: Bad, Evidence: []string{"ffmpeg ×3 300% of one core"}}}
		}
		return e
	}
	es := []Entry{mk(0, false, 0), mk(1, true, 40), mk(2, true, 71), mk(3, false, 0), mk(10, true, 20)}
	ins := Incidents(es)
	if len(ins) != 2 {
		t.Fatalf("incidents = %+v", ins)
	}
	if ins[0].Samples != 2 || !strings.Contains(ins[0].Peak, "71%") || !strings.Contains(ins[0].Evidence, "ffmpeg") {
		t.Fatalf("first = %+v", ins[0])
	}
}

func TestHistoryRoundTripAndPrune(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	now := time.Now()
	for _, e := range []Entry{{T: now.Add(-10 * 24 * time.Hour), CPU: 1}, {T: now.Add(-time.Hour), CPU: 2, Findings: []HFinding{{ID: "io-pressure", Sev: Warn}}}} {
		if err := appendEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := prune(historyPath(), now); err != nil {
		t.Fatal(err)
	}
	es, err := readHistory(historyPath(), now.Add(-24*time.Hour*30))
	if err != nil || len(es) != 1 || es[0].CPU != 2 || es[0].Findings[0].ID != "io-pressure" {
		t.Fatalf("es=%+v err=%v", es, err)
	}
	if fi, _ := os.Stat(historyPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("history must be private, mode = %v", fi.Mode())
	}
}

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 11, 25, 14, 0, 0, 0, time.Local)
	if got, _ := parseAt("03:12", now); got.Day() != 25 || got.Hour() != 3 {
		t.Fatalf("got %v", got)
	}
	if got, _ := parseAt("23:30", now); got.Day() != 24 {
		t.Fatalf("a time still in the future means yesterday, got %v", got)
	}
	if _, err := parseAt("soon", now); err == nil {
		t.Fatal("expected an error")
	}
}

func TestManyCopiesAreOneCause(t *testing.T) {
	f := newFixture(t)
	f.base("60.00", "0.00", "0.00", 12000000)
	for i := 0; i < 5; i++ {
		f.proc(100+i, "worker", 0, 100, 0, 0)
	}
	got := f.diagnose(func() {
		for i := 0; i < 5; i++ {
			f.proc(100+i, "worker", 100, 100, 0, 0)
		}
	})
	if len(got) != 1 || !strings.Contains(got[0].Evidence[0], "worker ×5 500% of one core") {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(got[0].Hint, "pkill -STOP -x worker") {
		t.Fatalf("hint = %q", got[0].Hint)
	}
}

func TestNeverSuggestsPausingTheDesktop(t *testing.T) {
	f := newFixture(t)
	f.base("60.00", "0.00", "0.00", 12000000)
	f.proc(7, "Hyprland", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(7, "Hyprland", 100, 100, 0, 0) })
	if len(got) != 1 || strings.Contains(got[0].Hint, "kill") || !strings.Contains(got[0].Hint, "do not pause") {
		t.Fatalf("hint = %q", got[0].Hint)
	}
}

func TestAMDDiscreteGPUAwakeAndBusy(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	// integrated Intel drives the screen, a discrete AMD card is awake and busy
	f.write("sys/bus/pci/devices/0000:00:02.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:00:02.0/vendor", "0x8086")
	f.write("sys/bus/pci/devices/0000:00:02.0/boot_vga", "1")
	f.write("sys/bus/pci/devices/0000:03:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:03:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:03:00.0/power/runtime_status", "active")
	f.write("sys/bus/pci/devices/0000:03:00.0/power/control", "auto")
	f.write("sys/bus/pci/devices/0000:03:00.0/gpu_busy_percent", "98")
	f.proc(60, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(60, "idle", 1, 100, 0, 0) })
	if ids(got) != "dgpu-awake,gpu-busy" && ids(got) != "gpu-busy,dgpu-awake" {
		t.Fatalf("got %s", ids(got))
	}
	if !strings.Contains(got[0].Title+got[1].Title, "AMD GPU is awake") || !strings.Contains(got[0].Title+got[1].Title, "AMD GPU is 98% busy") {
		t.Fatalf("titles = %q / %q", got[0].Title, got[1].Title)
	}
}

func TestSingleIntegratedGPUIsNeverReported(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/bus/pci/devices/0000:00:02.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:00:02.0/vendor", "0x8086")
	f.write("sys/bus/pci/devices/0000:00:02.0/boot_vga", "1")
	f.write("sys/bus/pci/devices/0000:00:02.0/power/runtime_status", "active")
	f.proc(60, "idle", 0, 100, 0, 0)
	if got := f.diagnose(func() { f.proc(60, "idle", 1, 100, 0, 0) }); len(got) != 0 {
		t.Fatalf("got %s", ids(got))
	}
}

func (f *fixture) wake(pid int, n uint64) {
	f.write(fmt.Sprintf("proc/%d/status", pid), fmt.Sprintf("Name:\tx\nvoluntary_ctxt_switches:\t%d\nnonvoluntary_ctxt_switches:\t5\n", n))
}

func TestIntelGPUBusyFromIdleResidency(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	dev := "sys/bus/pci/devices/0000:00:02.0/"
	f.write(dev+"class", "0x030000")
	f.write(dev+"vendor", "0x8086")
	f.write(dev+"boot_vga", "1")
	f.write(dev+"drm/card0/gt/gt0/rc6_residency_ms", "10000")
	f.proc(1, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() {
		f.write(dev+"drm/card0/gt/gt0/rc6_residency_ms", "10040") // idle 40 ms of the 1000 ms window: 96% busy
		f.proc(1, "idle", 1, 100, 0, 0)
	})
	if ids(got) != "gpu-busy" || !strings.Contains(got[0].Title, "Intel GPU is 96% busy") {
		t.Fatalf("got %s %+v", ids(got), got)
	}
}

func TestIntelGPUIdleIsQuiet(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	dev := "sys/bus/pci/devices/0000:00:02.0/"
	f.write(dev+"class", "0x030000")
	f.write(dev+"vendor", "0x8086")
	f.write(dev+"boot_vga", "1")
	f.write(dev+"tile0/gt0/gtidle/idle_residency_ms", "500") // the xe layout
	f.proc(1, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() {
		f.write(dev+"tile0/gt0/gtidle/idle_residency_ms", "1500")
		f.proc(1, "idle", 1, 100, 0, 0)
	})
	if len(got) != 0 {
		t.Fatalf("got %s", ids(got))
	}
}

func TestBatterySuspectsAreRankedByCPUAndWakeups(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/class/power_supply/BAT1/status", "Discharging")
	f.write("sys/class/power_supply/BAT1/capacity", "60")
	f.write("sys/class/power_supply/BAT1/power_now", "20000000")
	f.write("sys/class/power_supply/BAT1/energy_now", "40000000")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:65:00.0/boot_vga", "1")
	f.write("sys/bus/pci/devices/0000:65:00.0/hwmon/hwmon5/power1_average", "12200000")
	for i := 0; i < 3; i++ {
		f.proc(200+i, "firefox", 0, 100, 0, 0)
		f.wake(200+i, 0)
	}
	got := f.diagnose(func() {
		for i := 0; i < 3; i++ {
			f.proc(200+i, "firefox", 20, 100, 0, 0)
			f.wake(200+i, 100)
		}
	})
	if len(got) != 1 || got[0].ID != "battery-drain" {
		t.Fatalf("got %s", ids(got))
	}
	ev := strings.Join(got[0].Evidence, "\n")
	for _, want := range []string{"firefox ×3 60% of one core, 300 wake-ups/s", "AMD GPU alone draws 12.2 W", "not report watts per program"} {
		if !strings.Contains(ev, want) {
			t.Fatalf("missing %q in %s", want, ev)
		}
	}
}

func TestFrameListsTheBusiestPrograms(t *testing.T) {
	r := Report{Window: time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50},
		Procs: []Rate{{PID: 1, Comm: "a", CPU: 40}, {PID: 2, Comm: "b", CPU: 20}, {PID: 3, Comm: "b", CPU: 10}}}
	out := strings.Join(Frame(palette{false}, r, nil, &Hist{}, 80, "h", time.Now(), "", false), "\n")
	if !strings.Contains(out, "Busiest") || !strings.Contains(out, "a 40%") || !strings.Contains(out, "b ×2 30%") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestNVIDIARuntimeD3DisabledIsExplained(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/bus/pci/devices/0000:01:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:01:00.0/vendor", "0x10de")
	f.write("sys/bus/pci/devices/0000:01:00.0/power/runtime_status", "active")
	f.write("sys/bus/pci/devices/0000:01:00.0/power/control", "auto")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:65:00.0/boot_vga", "1")
	f.write("proc/driver/nvidia/gpus/0000:01:00.0/power", "Runtime D3 status:          Disabled by default\nVideo Memory:               Active\n")
	f.proc(60, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(60, "idle", 1, 100, 0, 0) })
	if ids(got) != "dgpu-awake" {
		t.Fatalf("got %s", ids(got))
	}
	ev := strings.Join(got[0].Evidence, "\n")
	if !strings.Contains(ev, "Runtime D3 (the sleep state) is disabled by default") || !strings.Contains(got[0].Hint, "NVreg_DynamicPowerManagement=0x02") {
		t.Fatalf("evidence=%q hint=%q", ev, got[0].Hint)
	}
}

func TestOnlyNotesStillLooksFine(t *testing.T) {
	r := Report{Window: time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50}}
	fs := []Finding{{ID: "dgpu-awake", Sev: Info, Title: "A GPU is awake", Why: "w"}}
	out := strings.Join(Frame(palette{false}, r, fs, &Hist{}, 80, "h", time.Now(), "", false), "\n")
	if !strings.Contains(out, "Everything looks fine") || !strings.Contains(out, "1 note") || !strings.Contains(out, "── Notes") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestConfigMuteAndFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := LoadConfig()
	if !c.Mute("dgpu-awake") || c.Mute("dgpu-awake") {
		t.Fatal("muting twice must report no change the second time")
	}
	if err := SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	c = LoadConfig()
	kept, muted := c.Filter([]Finding{{ID: "dgpu-awake"}, {ID: "cpu-pressure"}})
	if muted != 1 || len(kept) != 1 || kept[0].ID != "cpu-pressure" {
		t.Fatalf("kept=%v muted=%d", kept, muted)
	}
	if !c.Unmute("dgpu-awake") || c.Unmute("dgpu-awake") {
		t.Fatal("unmute")
	}
	if validID("nope") == nil || validID("cpu-pressure") != nil {
		t.Fatal("validID")
	}
}

func TestConfigThresholdsApplyAndReset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer applyThresholds(Config{})
	if err := SaveConfig(Config{DrainWatts: 40, HotC: 95}); err != nil {
		t.Fatal(err)
	}
	LoadConfig()
	if drainWatts != 40 || hotC != 95 || psiWarn != 10 {
		t.Fatalf("drain=%v hot=%v warn=%v", drainWatts, hotC, psiWarn)
	}
	applyThresholds(Config{})
	if drainWatts != 15 {
		t.Fatal("defaults must come back")
	}
}

func TestBrokenConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "kerna"), 0o755)
	os.WriteFile(filepath.Join(dir, "kerna", "config.json"), []byte("{not json"), 0o644)
	if c := LoadConfig(); len(c.Ignore) != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestNotifierWaitsAMinuteAndDoesNotRepeat(t *testing.T) {
	n := newNotifier()
	bad := []Finding{{ID: "cpu-pressure", Sev: Bad}}
	now := int64(1000)
	for i := 1; i < notifyAfter; i++ {
		if got := n.observe(bad, now); len(got) != 0 {
			t.Fatalf("sample %d must not notify yet", i)
		}
		now += 15
	}
	if got := n.observe(bad, now); len(got) != 1 {
		t.Fatal("the fourth sample in a row must notify")
	}
	now += 15
	if got := n.observe(bad, now); len(got) != 0 {
		t.Fatal("it must not repeat within 30 minutes")
	}
	n.observe(nil, now+15) // the problem went away
	for i := 0; i < notifyAfter; i++ {
		now += notifyRepeat
		n.observe(bad, now)
	}
	if got := n.observe(bad, now+notifyRepeat); len(got) != 1 {
		t.Fatal("after 30 minutes it may notify again")
	}
	if got := newNotifier().observe([]Finding{{ID: "x", Sev: Info}, {ID: "x", Sev: Info}, {ID: "x", Sev: Info}, {ID: "x", Sev: Info}}, 0); len(got) != 0 {
		t.Fatal("notes never notify")
	}
}

func TestWaybarJSON(t *testing.T) {
	var o waybarOut
	if err := json.Unmarshal([]byte(Waybar(nil)), &o); err != nil || o.Class != "ok" || !strings.Contains(o.Text, "ok") {
		t.Fatalf("ok: %+v %v", o, err)
	}
	fs := []Finding{{ID: "a", Sev: Warn, Title: "A & B <x>", Evidence: []string{"e1"}}, {ID: "b", Sev: Bad, Title: "T2"}, {ID: "c", Sev: Info, Title: "N"}}
	if err := json.Unmarshal([]byte(Waybar(fs)), &o); err != nil {
		t.Fatal(err)
	}
	if o.Class != "bad" || o.Text != "● 2" || !strings.Contains(o.Tooltip, "A &amp; B &lt;x&gt;") || !strings.Contains(o.Tooltip, "› e1") {
		t.Fatalf("%+v", o)
	}
	json.Unmarshal([]byte(Waybar([]Finding{{ID: "c", Sev: Info, Title: "N"}})), &o)
	if o.Class != "note" || o.Text != "●" {
		t.Fatalf("note: %+v", o)
	}
}

func TestPowerProfileOnBattery(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/firmware/acpi/platform_profile", "performance")
	f.write("sys/class/power_supply/BAT1/status", "Discharging")
	f.write("sys/class/power_supply/BAT1/capacity", "60")
	f.write("sys/class/power_supply/BAT1/power_now", "9000000")
	f.write("sys/class/power_supply/BAT1/energy_now", "40000000")
	f.proc(1, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(1, "idle", 1, 100, 0, 0) })
	if ids(got) != "power-profile" || got[0].Sev != Info || !strings.Contains(got[0].Hint, "powerprofilesctl set balanced") {
		t.Fatalf("got %s %+v", ids(got), got)
	}
	f.write("sys/class/power_supply/BAT1/status", "Charging")
	if got := f.diagnose(func() { f.proc(1, "idle", 2, 100, 0, 0) }); len(got) != 0 {
		t.Fatalf("plugged in must be quiet, got %s", ids(got))
	}
}

func TestCompareShowsBetterWorseAndFixed(t *testing.T) {
	a := Snap{T: time.Now(), Watts: 33.8, Temp: 60, CPU: 10, GPUState: map[string]string{"NVIDIA": "active"}, Findings: []string{"dgpu-awake", "battery-drain"}}
	b := Snap{T: time.Now(), Watts: 28.0, Temp: 60.5, CPU: 20, GPUState: map[string]string{"NVIDIA": "suspended"}, Findings: []string{"battery-drain", "cpu-pressure"}}
	out := strings.Join(CompareLines(palette{false}, a, b), "\n")
	for _, want := range []string{"↓ 5.8 W better", "Temperature", "same", "↑ 10.0% worse", "NVIDIA GPU", "suspended ✔", "gone: A discrete GPU was awake", "new: Programs were waiting for the CPU"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSnapshotRoundTripAndNameCheck(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := SaveSnap("before-fix", Snap{Watts: 12}); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSnap("before-fix")
	if err != nil || s.Watts != 12 {
		t.Fatalf("%+v %v", s, err)
	}
	if SaveSnap("../evil", Snap{}) == nil {
		t.Fatal("a path in the name must be refused")
	}
	if _, err := LoadSnap("missing"); err == nil {
		t.Fatal("expected an error")
	}
}

const sampleKernelLog = `2026-11-25T03:08:11+03:00 host kernel: amdgpu 0000:65:00.0: amdgpu: ring gfx_0.0.0 timeout, signaled seq=1 emitted seq=3
2026-11-25T03:08:11+03:00 host kernel: amdgpu 0000:65:00.0: amdgpu: GPU reset begin!
2026-11-25T03:10:55+03:00 host kernel: Out of memory: Killed process 4242 (firefox) total-vm:9000000kB
2026-11-25T03:11:02+03:00 host kernel: usb 1-1: new device
`

func TestScanEventsFindsAndMergesBursts(t *testing.T) {
	ev := ScanEvents(sampleKernelLog)
	if len(ev) != 2 || ev[0].Kind != "gpu" || ev[1].Kind != "oom" {
		t.Fatalf("events = %+v", ev)
	}
	if !strings.Contains(ev[1].Line, "Killed process 4242 (firefox)") || strings.Contains(ev[1].Line, "2026-11-25") {
		t.Fatalf("line = %q", ev[1].Line)
	}
}

func fakeJournal(kernel, oomd, tail string) {
	journal = func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "-k"):
			return kernel, nil
		case strings.Contains(joined, "systemd-oomd"):
			return oomd, nil
		case strings.Contains(joined, "-n 400"):
			return tail, nil
		}
		return "", fmt.Errorf("unexpected %s", joined)
	}
}

func TestWhyFrozeNamesTheLastCauseBeforeAnUncleanEnd(t *testing.T) {
	defer func(old func(...string) (string, error)) { journal = old }(journal)
	fakeJournal(sampleKernelLog, "", "2026-11-25T03:11:00+03:00 host systemd[1]: something\n2026-11-25T03:11:20+03:00 host kernel: usb 1-1: new device\n")
	b := scanBoot(-1)
	if !b.Checked || b.Clean {
		t.Fatalf("an end without a shutdown message is unclean: %+v", b)
	}
	kind, ev, ok := Cause(b)
	if !ok || kind != "oom" || !strings.Contains(ev.Line, "firefox") {
		t.Fatalf("cause = %q %+v %v", kind, ev, ok)
	}
	out := strings.Join(FrozeLines(palette{false}, []BootReport{b}, nil), "\n")
	for _, want := range []string{"ended without a normal shutdown", "Most likely: Memory ran out and a program was killed", "kerna has no history"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRealShutdownMarkersCountAsClean(t *testing.T) {
	// taken from a real journal: no "Reached target", only these
	tail := "2026-11-25T03:00:08+03:00 vamola systemd[1]: Stopped target Local File Systems.\n2026-11-25T03:00:08+03:00 vamola systemd[1]: Unmounted /boot.\n"
	if clean, ended := EndOfBoot(tail); !clean || ended.Hour() != 3 {
		t.Fatalf("clean=%v ended=%v", clean, ended)
	}
	if clean, _ := EndOfBoot("2026-11-25T03:11:20+03:00 host kernel: usb 1-1: new device\n"); clean {
		t.Fatal("an ordinary line is not a shutdown")
	}
}

func TestWhyFrozeCleanShutdownAndNoCause(t *testing.T) {
	defer func(old func(...string) (string, error)) { journal = old }(journal)
	fakeJournal("", "", "2026-11-25T03:00:07+03:00 host systemd[1]: Reached target System Power Off.\n2026-11-25T03:00:08+03:00 host systemd-shutdown[1]: Syncing filesystems\n")
	b := scanBoot(-1)
	if !b.Clean {
		t.Fatalf("%+v", b)
	}
	if _, _, ok := Cause(b); ok {
		t.Fatal("a clean shutdown has no cause")
	}
	fakeJournal(sampleKernelLog, "", "2026-11-25T05:30:00+03:00 host kernel: usb 1-1: new device\n")
	b = scanBoot(-1)
	if _, _, ok := Cause(b); ok {
		t.Fatal("events hours before the end must not be blamed")
	}
	out := strings.Join(FrozeLines(palette{false}, []BootReport{b}, nil), "\n")
	if !strings.Contains(out, "hard freeze or a power loss") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestChargingRowIsNotShownAsConsumption(t *testing.T) {
	r := Report{Window: time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50, Battery: &Battery{Name: "BAT1", Status: "Charging", Percent: 31, Watts: 81.3}}}
	out := strings.Join(Frame(palette{false}, r, nil, &Hist{}, 80, "h", time.Now(), "", false), "\n")
	if !strings.Contains(out, "Charging") || !strings.Contains(out, "+81.3 W") || strings.Contains(out, "Power ") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestTurkishUsesDecimalComma(t *testing.T) {
	old := lang
	lang = "tr"
	defer func() { lang = old }()
	if fl(28.34, 1) != "28,3" || fl(5, 0) != "5" {
		t.Fatalf("got %q %q", fl(28.34, 1), fl(5, 0))
	}
	lang = "en"
	if fl(28.34, 1) != "28.3" {
		t.Fatal("english keeps the dot")
	}
}

func TestParseNetDevAndSNMP(t *testing.T) {
	dev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 500 5 0 0 0 0 0 0 500 5 0 0 0 0 0 0
wlp3s0: 1000 10 1 2 0 0 0 0 2000 20 0 3 0 0 0 0
docker0: 9999 99 0 0 0 0 0 0 9999 99 0 0 0 0 0 0
`
	c := parseNetDev(dev)
	if c.Rx != 1000 || c.Tx != 2000 || c.Pkts != 30 || c.Bad != 6 {
		t.Fatalf("%+v", c)
	}
	out, re := parseSNMPTcp("Tcp: RtoAlgorithm OutSegs RetransSegs InErrs\nTcp: 1 4000 120 0\n")
	if out != 4000 || re != 120 {
		t.Fatalf("%d %d", out, re)
	}
}

func TestPacketLossFinding(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("proc/net/snmp", "Tcp: OutSegs RetransSegs\nTcp: 1000 10\n")
	f.write("proc/net/dev", " face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n eth0: 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
	f.proc(1, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() {
		f.write("proc/net/snmp", "Tcp: OutSegs RetransSegs\nTcp: 2000 110\n") // 1000 sent, 100 again: 10%
		f.proc(1, "idle", 1, 100, 0, 0)
	})
	if ids(got) != "net-loss" || got[0].Sev != Bad || !strings.Contains(got[0].Why, "10.0%") {
		t.Fatalf("got %s %+v", ids(got), got)
	}
}

func TestWeakWifiOnlyWhenTheDriverReportsIt(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("proc/net/wireless", "Inter-| sta-|   Quality        |   Discarded packets\n face | tus | link level noise |  nwid\nwlan0: 0000   40.  -78.  -256        0\n")
	f.proc(1, "idle", 0, 100, 0, 0)
	got := f.diagnose(func() { f.proc(1, "idle", 1, 100, 0, 0) })
	if ids(got) != "wifi-weak" || !strings.Contains(got[0].Why, "-78 dBm") {
		t.Fatalf("got %s", ids(got))
	}
	f.write("proc/net/wireless", "Inter-| sta-|   Quality        |   Discarded packets\n face | tus | link level noise |  nwid\n")
	if got := f.diagnose(func() { f.proc(1, "idle", 2, 100, 0, 0) }); len(got) != 0 {
		t.Fatalf("an empty file means unknown, got %s", ids(got))
	}
}

func TestDescribeAndLabel(t *testing.T) {
	if got := label("qs", 1248, 1); got != "qs (pid 1248) · Quickshell, your desktop shell" {
		t.Fatalf("got %q", got)
	}
	if got := label("ffmpeg", 7, 3); got != "ffmpeg ×3" {
		t.Fatalf("got %q", got)
	}
}

func TestCommandOf(t *testing.T) {
	if got := commandOf(Finding{Hint: "echo auto | sudo tee /x   # until reboot"}); got != "echo auto | sudo tee /x" {
		t.Fatalf("got %q", got)
	}
	if commandOf(Finding{}) != "" {
		t.Fatal("no hint, no command")
	}
}

func TestFrameMarksTheSelectedFindingAndListsPrograms(t *testing.T) {
	r := Report{Window: time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50},
		Procs: []Rate{{PID: 1, Comm: "qs", CPU: 40, Wakeups: 300, RSSKiB: 2 << 20}}}
	fs := []Finding{{ID: "a", Sev: Warn, Title: "First", Why: "w"}, {ID: "b", Sev: Warn, Title: "Second", Why: "w"}}
	out := strings.Join(FrameOpts(palette{false}, r, fs, &Hist{}, 90, "h", time.Now(), "", true, Opts{Sel: 1, Top: true}), "\n")
	for _, want := range []string{"▸ Second", "● First", "── Programs", "qs · Quickshell, your desktop shell", "40%", "300", "2.0 GiB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTodaySummary(t *testing.T) {
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.Local)
	var es []Entry
	for i := 0; i < 240; i++ { // one hour of samples at 14:xx on battery
		es = append(es, Entry{T: time.Date(2026, 11, 25, 14, 0, 0, 0, time.Local).Add(time.Duration(i) * recordEvery), Watts: 20, GPUWatts: 10, Top: []HTop{{N: "qs", C: 40}, {N: "firefox", C: 20}}})
	}
	es = append(es, Entry{T: now.Add(-48 * time.Hour), Watts: 99}) // yesterday: ignored
	out := strings.Join(TodayLines(palette{false}, es, now), "\n")
	for _, want := range []string{"Recorded 1.0 hours, 240 samples", "about 20 Wh used", "average 20.0 W", "qs · Quickshell", "40%", "No problems today", "The GPU drivers reported 10.0 W"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "99") {
		t.Fatalf("yesterday leaked into today:\n%s", out)
	}
}

func TestSleepGapsAndModes(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 23, 0, 0, 0, time.Local)
	es := []Entry{
		{T: t0, Bat: 80},
		{T: t0.Add(8 * time.Hour), Bat: 62}, // 18 points in 8 h: 2.25 %/h
		{T: t0.Add(8*time.Hour + recordEvery), Bat: 62},
		{T: t0.Add(9 * time.Hour), Bat: 61, Plugged: true}, // plugged: not a sleep gap
		{T: t0.Add(20 * time.Hour), Bat: 90, Plugged: true},
	}
	gaps := SleepGaps(es)
	if len(gaps) != 1 || gaps[0].Rate() < 2.2 || gaps[0].Rate() > 2.3 {
		t.Fatalf("gaps = %+v", gaps)
	}
	sel, deep := selectedSleep("[s2idle] deep")
	if sel != "s2idle" || !deep {
		t.Fatalf("%q %v", sel, deep)
	}
	st := SleepStats{Success: 3, Fail: 1, LastFailedDev: "0000:01:00.0", LastFailedStep: "suspend", MemSleep: "[s2idle] deep", Known: true}
	out := strings.Join(SleepLines(palette{false}, st, es), "\n")
	for _, want := range []string{"3 succeeded, 1 failed", "device 0000:01:00.0", "mem_sleep_default=deep", "2.2%/h", "high"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSleepStatsFromSysfs(t *testing.T) {
	f := newFixture(t)
	f.write("sys/power/suspend_stats/success", "12")
	f.write("sys/power/suspend_stats/fail", "0")
	f.write("sys/power/mem_sleep", "s2idle [deep]")
	st := Source{Root: f.root}.readSleep()
	if !st.Known || st.Success != 12 || st.MemSleep != "s2idle [deep]" {
		t.Fatalf("%+v", st)
	}
}

const fdinfoAMD = `pos:	0
flags:	02000002
drm-driver:	amdgpu
drm-pdev:	0000:65:00.0
drm-client-id:	%d
drm-engine-gfx:	%d ns
drm-engine-compute:	0 ns
drm-engine-dec:	%d ns
drm-engine-capacity-gfx:	1
drm-memory-vram:	100 KiB
`

func (f *fixture) gpuProc(pid int, comm string, client int, gfxNs, decNs uint64) {
	f.proc(pid, comm, 0, 100, 0, 0)
	dir := filepath.Join(f.root, fmt.Sprintf("proc/%d/fd", pid))
	os.MkdirAll(dir, 0o755)
	os.Remove(filepath.Join(dir, "7"))
	os.Symlink("/dev/dri/renderD128", filepath.Join(dir, "7"))
	os.Remove(filepath.Join(dir, "8")) // the same client through a duplicated descriptor
	os.Symlink("/dev/dri/renderD128", filepath.Join(dir, "8"))
	f.write(fmt.Sprintf("proc/%d/fdinfo/7", pid), fmt.Sprintf(fdinfoAMD, client, gfxNs, decNs))
	f.write(fmt.Sprintf("proc/%d/fdinfo/8", pid), fmt.Sprintf(fdinfoAMD, client, gfxNs, decNs))
}

func TestParseDRMFdinfo(t *testing.T) {
	id, ns, ok := parseDRMFdinfo(fmt.Sprintf(fdinfoAMD, 42, 3000, 500))
	if !ok || id != "42" || ns != 3500 {
		t.Fatalf("id=%q ns=%d ok=%v (capacity lines and memory must not count)", id, ns, ok)
	}
	if _, _, ok := parseDRMFdinfo("pos: 0\nflags: 02\n"); ok {
		t.Fatal("a file without drm-driver is not DRM")
	}
}

func TestGPUTimePerProgram(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/class/drm/card0/dev", "226:0")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:65:00.0/boot_vga", "1")
	f.write("sys/bus/pci/devices/0000:65:00.0/gpu_busy_percent", "69")
	f.write("sys/bus/pci/devices/0000:65:00.0/hwmon/hwmon5/power1_average", "14000000")
	f.write("sys/class/power_supply/BAT1/status", "Discharging")
	f.write("sys/class/power_supply/BAT1/capacity", "50")
	f.write("sys/class/power_supply/BAT1/power_now", "30000000")
	f.write("sys/class/power_supply/BAT1/energy_now", "40000000")
	f.gpuProc(1, "Hyprland", 1, 0, 0)
	f.gpuProc(2, "firefox", 2, 0, 0)
	f.gpuProc(3, "quiet", 3, 0, 0)
	got := f.diagnose(func() {
		f.gpuProc(1, "Hyprland", 1, 250_000_000, 0)         // 25% of a 1 s window
		f.gpuProc(2, "firefox", 2, 100_000_000, 60_000_000) // 16%: the duplicated descriptor must count once
		f.gpuProc(3, "quiet", 3, 1_000_000, 0)
	})
	var load *Finding
	for i := range got {
		if got[i].ID == "gpu-load" {
			load = &got[i]
		}
	}
	if load == nil || load.Sev != Warn || !strings.Contains(load.Title, "AMD GPU draws 14 W (69% busy)") {
		t.Fatalf("got %s %+v", ids(got), got)
	}
	ev := strings.Join(load.Evidence, "\n")
	if !strings.Contains(ev, "Hyprland (pid 1) · your window manager: 25% of the GPU") || !strings.Contains(ev, "firefox (pid 2): 16% of the GPU") || strings.Contains(ev, "quiet") {
		t.Fatalf("evidence:\n%s", ev)
	}
	if !strings.Contains(ev, "only root can see") {
		t.Fatalf("41%% visible of 69%% busy must mention the invisible rest:\n%s", ev)
	}
}

func TestIdleGPUHasNoLoadFinding(t *testing.T) {
	f := newFixture(t)
	f.base("0.00", "0.00", "0.00", 12000000)
	f.write("sys/class/drm/card0/dev", "226:0")
	f.write("sys/bus/pci/devices/0000:65:00.0/class", "0x030000")
	f.write("sys/bus/pci/devices/0000:65:00.0/vendor", "0x1002")
	f.write("sys/bus/pci/devices/0000:65:00.0/boot_vga", "1")
	f.write("sys/bus/pci/devices/0000:65:00.0/gpu_busy_percent", "10")
	f.gpuProc(1, "Hyprland", 1, 0, 0)
	if got := f.diagnose(func() { f.gpuProc(1, "Hyprland", 1, 300_000_000, 0) }); len(got) != 0 {
		t.Fatalf("a quiet GPU must stay quiet, got %s", ids(got))
	}
}

func TestFrameShowsGPUUsersAndColumn(t *testing.T) {
	r := Report{Window: time.Second, PSI: map[string]PSI{}, Static: Static{MemTotalKiB: 100, MemAvailKiB: 50, GPUs: []GPU{{Vendor: "amd", Runtime: "active", BusyPct: 60, Watts: 12}}},
		Procs: []Rate{{PID: 1, Comm: "Hyprland", CPU: 10, GPU: 25}, {PID: 2, Comm: "firefox", CPU: 20, GPU: 17}}}
	out := strings.Join(FrameOpts(palette{false}, r, nil, &Hist{}, 96, "h", time.Now(), "", true, Opts{Sel: -1, Top: true}), "\n")
	for _, want := range []string{"GPU users", "Hyprland 25%", "firefox 17%", "GPU", "25%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// whyProc writes a process with explicit parent, start time and threads.
func (f *fixture) whyProc(pid, ppid int, comm string, startTicks, ticks uint64, threads map[string]uint64) {
	stat := func(id int, name string, tk uint64) string {
		return fmt.Sprintf("%d (%s) S %d 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 %d 1 2500 0", id, name, ppid, tk, startTicks)
	}
	f.write(fmt.Sprintf("proc/%d/stat", pid), stat(pid, comm, ticks))
	f.write(fmt.Sprintf("proc/%d/comm", pid), comm+"\n")
	f.write(fmt.Sprintf("proc/%d/cmdline", pid), comm+"\x00-c\x00cfg\x00")
	f.write(fmt.Sprintf("proc/%d/status", pid), "voluntary_ctxt_switches:\t0\n")
	i := 0
	for name, tk := range threads {
		tid := pid*10 + i
		f.write(fmt.Sprintf("proc/%d/task/%d/stat", pid, tid), stat(tid, name, tk))
		f.write(fmt.Sprintf("proc/%d/task/%d/comm", pid, tid), name+"\n")
		i++
	}
}

func TestWhyShowsLifetimeVersusNowThreadsAndChildren(t *testing.T) {
	f := newFixture(t)
	f.write("proc/uptime", "1000.00 5000.00\n") // up 1000 s
	// qs started at tick 0 (so it ran 1000 s) and has used 33000 ticks = 330 s: 33% over its life
	f.whyProc(500, 1, "qs", 0, 33000, map[string]uint64{"QSGRenderThread": 10000, "qs": 23000})
	f.whyProc(501, 500, "cava", 50000, 100, map[string]uint64{"cava": 100})
	src := Source{Root: f.root}
	done := make(chan struct{})
	var rep WhyReport
	var ok bool
	go func() { rep, ok = src.Why("qs", 300*time.Millisecond); close(done) }()
	time.Sleep(100 * time.Millisecond)
	// 60% of one core in the render thread during the window
	f.whyProc(500, 1, "qs", 0, 33000+60, map[string]uint64{"QSGRenderThread": 10000 + 55, "qs": 23000 + 5})
	f.whyProc(501, 500, "cava", 50000, 100+5, map[string]uint64{"cava": 105})
	<-done
	if !ok || rep.Comm != "qs" || len(rep.PIDs) != 1 {
		t.Fatalf("%+v ok=%v", rep, ok)
	}
	if rep.LifeCPU < 32 || rep.LifeCPU > 34.5 {
		t.Fatalf("lifetime = %.1f, want about 33", rep.LifeCPU)
	}
	if rep.Age < 990*time.Second || rep.Age > 1010*time.Second {
		t.Fatalf("age = %v", rep.Age)
	}
	if len(rep.Threads) == 0 || rep.Threads[0].Name != "QSGRenderThread" {
		t.Fatalf("threads = %+v", rep.Threads)
	}
	if len(rep.Children) != 1 || rep.Children[0].Comm != "cava" {
		t.Fatalf("children = %+v", rep.Children)
	}
	out := strings.Join(WhyLines(palette{false}, rep), "\n")
	for _, want := range []string{"Quickshell, your desktop shell", "average over its whole life", "Busiest threads", "QSGRenderThread", "Child programs", "cava"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWhyObservations(t *testing.T) {
	w := WhyReport{LifeCPU: 46, NowCPU: 10, Threads: []threadUse{{"qs", 3}}}
	if o := strings.Join(w.Observations(), "\n"); !strings.Contains(o, "much higher than the 10% right now") {
		t.Fatalf("lifetime vs now: %s", o)
	}
	w = WhyReport{LifeCPU: 50, NowCPU: 60, Threads: []threadUse{{"QSGRenderThread", 40}, {"qs", 20}}, GPU: 30, WakeupsPS: 900,
		Children: []childUse{{"cava", 7, 4.7}}}
	o := strings.Join(w.Observations(), "\n")
	for _, want := range []string{`thread "QSGRenderThread": it keeps redrawing`, "GPU busy: 30%", "runs cava (pid 7)", "900 times a second"} {
		if !strings.Contains(o, want) {
			t.Fatalf("missing %q in %s", want, o)
		}
	}
	if o := strings.Join(WhyReport{NowCPU: 2}.Observations(), ""); !strings.Contains(o, "Nothing unusual") {
		t.Fatalf("quiet: %s", o)
	}
}

func TestWhyFindsByPartOfTheNameAndPid(t *testing.T) {
	f := newFixture(t)
	f.write("proc/uptime", "100 0\n")
	f.whyProc(7, 1, "Isolated Web Co", 0, 100, map[string]uint64{"Isolated Web Co": 100})
	src := Source{Root: f.root}
	if got := src.findProcs("web"); len(got) != 1 || got[0] != 7 {
		t.Fatalf("part of the name: %v", got)
	}
	if got := src.findProcs("7"); len(got) != 1 {
		t.Fatalf("pid: %v", got)
	}
	if got := src.findProcs("nothing-here"); len(got) != 0 {
		t.Fatalf("no match: %v", got)
	}
}
