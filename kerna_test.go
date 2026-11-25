package main

import (
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
