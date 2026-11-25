package main

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// clkTck is the kernel's USER_HZ. It is 100 on every mainstream Linux build.
const clkTck = 100

// Source reads kernel files below Root. Root is "/" for real use and a
// fixture directory in tests, so every parser can be tested without a machine.
type Source struct{ Root string }

func (s Source) p(parts ...string) string {
	return filepath.Join(append([]string{s.Root}, parts...)...)
}

func (s Source) read(parts ...string) (string, bool) {
	b, err := os.ReadFile(s.p(parts...))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func (s Source) readInt(parts ...string) (int64, bool) {
	v, ok := s.read(parts...)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 0, 64)
	return n, err == nil
}

// Proc is one process at one moment.
type Proc struct {
	PID      int
	Comm     string
	Ticks    uint64 // utime + stime
	RSSKiB   int64
	ReadB    uint64
	WriteB   uint64
	HasIO    bool   // /proc/PID/io is readable (own processes only without root)
	HoldsGPU bool   // has an NVIDIA device file open (own processes only)
	Wakeups  uint64 // voluntary context switches: how often it went to sleep and was woken again
}

// PSI is one line of /proc/pressure/*: how long tasks were stalled.
type PSI struct{ SomeAvg10, FullAvg10 float64 }

// Raw is everything read at one instant.
type Raw struct {
	At        time.Time
	CPUBusy   uint64 // jiffies, all CPUs
	CPUTotal  uint64
	Procs     map[int]Proc
	PSI       map[string]PSI
	SwapPages uint64 // pswpin + pswpout
	MajFaults uint64
	GPUIdleMs map[string]uint64 // Intel: milliseconds spent in the GPU's idle state, per PCI address
}

// Static readings that do not need two samples.
type Static struct {
	CPUs         int
	Load1        float64
	MemTotalKiB  int64
	MemAvailKiB  int64
	SwapTotalKiB int64
	SwapFreeKiB  int64
	MaxTempC     float64
	MaxTempZone  string
	FreqRatio    float64 // current / max, averaged over CPUs; 0 if unknown
	Battery      *Battery
	GPUs         []GPU
}

type Battery struct {
	Name      string
	Status    string // Charging, Discharging, Full...
	Percent   int
	Watts     float64
	HoursLeft float64 // 0 when unknown
}

type GPU struct {
	Addr     string
	Vendor   string  // "nvidia", "amd", "intel", other hex
	Runtime  string  // active, suspended, ...
	Control  string  // "auto" lets the kernel power the GPU down, "on" keeps it always on
	Boot     bool    // boot_vga: the GPU driving the built-in display on most laptops
	BusyPct  int     // how busy the GPU is: amdgpu reports it, Intel is derived from idle residency; -1 when unknown
	Watts    float64 // amdgpu reports its power draw; 0 when unknown
	NvidiaD3 string  // the NVIDIA driver's own "Runtime D3 status", e.g. "Disabled by default"
}

func parseStatLine(line string) (Proc, bool) {
	l := strings.IndexByte(line, '(')
	r := strings.LastIndexByte(line, ')')
	if l < 0 || r < l {
		return Proc{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:l]))
	if err != nil {
		return Proc{}, false
	}
	f := strings.Fields(line[r+1:]) // f[0] is field 3 (state)
	if len(f) < 22 {
		return Proc{}, false
	}
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ := strconv.ParseInt(f[21], 10, 64)
	return Proc{PID: pid, Comm: line[l+1 : r], Ticks: ut + st, RSSKiB: rss * int64(os.Getpagesize()) / 1024}, true
}

func parsePSI(text string) PSI {
	var p PSI
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		for _, kv := range f[1:] {
			if strings.HasPrefix(kv, "avg10=") {
				v, _ := strconv.ParseFloat(kv[6:], 64)
				if f[0] == "some" {
					p.SomeAvg10 = v
				} else if f[0] == "full" {
					p.FullAvg10 = v
				}
			}
		}
	}
	return p
}

func (s Source) readRaw() Raw {
	r := Raw{At: time.Now(), Procs: map[int]Proc{}, PSI: map[string]PSI{}}
	if f, err := os.Open(s.p("proc/stat")); err == nil {
		sc := bufio.NewScanner(f)
		if sc.Scan() {
			v := strings.Fields(sc.Text()) // cpu user nice system idle iowait irq softirq steal
			var sum, idle uint64
			for i, x := range v[1:] {
				n, _ := strconv.ParseUint(x, 10, 64)
				if i < 8 {
					sum += n
				}
				if i == 3 || i == 4 {
					idle += n
				}
			}
			r.CPUTotal, r.CPUBusy = sum, sum-idle
		}
		f.Close()
	}
	for _, k := range []string{"cpu", "io", "memory"} {
		if t, ok := s.read("proc/pressure", k); ok {
			r.PSI[k] = parsePSI(t)
		}
	}
	if f, err := os.Open(s.p("proc/vmstat")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			v := strings.Fields(sc.Text())
			if len(v) != 2 {
				continue
			}
			n, _ := strconv.ParseUint(v[1], 10, 64)
			switch v[0] {
			case "pswpin", "pswpout":
				r.SwapPages += n
			case "pgmajfault":
				r.MajFaults = n
			}
		}
		f.Close()
	}
	_, nvErr := os.Stat(s.p("proc/driver/nvidia")) // only scan open files when an NVIDIA driver exists
	scanFD := nvErr == nil
	entries, _ := os.ReadDir(s.p("proc"))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(s.p("proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		pr, ok := parseStatLine(string(b))
		if !ok {
			continue
		}
		pr.PID = pid
		if t, ok := s.read("proc", e.Name(), "status"); ok {
			for _, line := range strings.Split(t, "\n") {
				if v, found := strings.CutPrefix(line, "voluntary_ctxt_switches:"); found {
					pr.Wakeups, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
					break
				}
			}
		}
		if t, ok := s.read("proc", e.Name(), "io"); ok {
			for _, line := range strings.Split(t, "\n") {
				k, v, _ := strings.Cut(line, ": ")
				n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
				switch k {
				case "read_bytes":
					pr.ReadB, pr.HasIO = n, true
				case "write_bytes":
					pr.WriteB = n
				}
			}
		}
		if !scanFD {
			r.Procs[pid] = pr
			continue
		}
		if fds, err := os.ReadDir(s.p("proc", e.Name(), "fd")); err == nil {
			for _, fd := range fds {
				if dst, err := os.Readlink(s.p("proc", e.Name(), "fd", fd.Name())); err == nil && strings.HasPrefix(dst, "/dev/nvidia") {
					pr.HoldsGPU = true
					break
				}
			}
		}
		r.Procs[pid] = pr
	}
	r.GPUIdleMs = s.readGPUIdle()
	return r
}

// gpuIdleFiles are where Intel drivers publish the time the GPU spent idle:
// i915 (older and newer layouts) and xe.
var gpuIdleFiles = []string{
	"drm/card*/gt/gt0/rc6_residency_ms",
	"drm/card*/power/rc6_residency_ms",
	"tile0/gt0/gtidle/idle_residency_ms",
}

func (s Source) readGPUIdle() map[string]uint64 {
	out := map[string]uint64{}
	devs, _ := filepath.Glob(s.p("sys/bus/pci/devices/*"))
	for _, d := range devs {
		rel := strings.TrimPrefix(d, s.Root)
		if v, _ := s.read(rel, "vendor"); v != "0x8086" {
			continue
		}
		if c, _ := s.read(rel, "class"); !strings.HasPrefix(c, "0x03") {
			continue
		}
		for _, pat := range gpuIdleFiles {
			m, _ := filepath.Glob(filepath.Join(d, pat))
			if len(m) == 0 {
				continue
			}
			if txt, ok := s.read(strings.TrimPrefix(m[0], s.Root)); ok {
				if n, err := strconv.ParseUint(txt, 10, 64); err == nil {
					out[filepath.Base(d)] = n
					break
				}
			}
		}
	}
	return out
}

func (s Source) readStatic() Static {
	st := Static{}
	if entries, err := os.ReadDir(s.p("sys/devices/system/cpu")); err == nil {
		var ratioSum float64
		var ratioN int
		for _, e := range entries {
			n := e.Name()
			if len(n) > 3 && strings.HasPrefix(n, "cpu") && n[3] >= '0' && n[3] <= '9' {
				st.CPUs++
				cur, ok1 := s.readInt("sys/devices/system/cpu", n, "cpufreq/scaling_cur_freq")
				max, ok2 := s.readInt("sys/devices/system/cpu", n, "cpufreq/cpuinfo_max_freq")
				if ok1 && ok2 && max > 0 {
					ratioSum += float64(cur) / float64(max)
					ratioN++
				}
			}
		}
		if ratioN > 0 {
			st.FreqRatio = ratioSum / float64(ratioN)
		}
	}
	if t, ok := s.read("proc/loadavg"); ok {
		st.Load1, _ = strconv.ParseFloat(strings.Fields(t)[0], 64)
	}
	if f, err := os.Open(s.p("proc/meminfo")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			v := strings.Fields(sc.Text())
			if len(v) < 2 {
				continue
			}
			n, _ := strconv.ParseInt(v[1], 10, 64)
			switch v[0] {
			case "MemTotal:":
				st.MemTotalKiB = n
			case "MemAvailable:":
				st.MemAvailKiB = n
			case "SwapTotal:":
				st.SwapTotalKiB = n
			case "SwapFree:":
				st.SwapFreeKiB = n
			}
		}
		f.Close()
	}
	zones, _ := filepath.Glob(s.p("sys/class/thermal/thermal_zone*"))
	for _, z := range zones {
		t, ok := s.readInt(strings.TrimPrefix(z, s.Root), "temp")
		if !ok {
			continue
		}
		if c := float64(t) / 1000; c > st.MaxTempC && c < 150 {
			st.MaxTempC = c
			st.MaxTempZone, _ = s.read(strings.TrimPrefix(z, s.Root), "type")
		}
	}
	st.Battery = s.readBattery()
	st.GPUs = s.readGPUs()
	return st
}

func (s Source) readBattery() *Battery {
	dirs, _ := filepath.Glob(s.p("sys/class/power_supply/BAT*"))
	if len(dirs) == 0 {
		return nil
	}
	d := strings.TrimPrefix(dirs[0], s.Root)
	b := &Battery{Name: filepath.Base(d)}
	b.Status, _ = s.read(d, "status")
	pct, _ := s.readInt(d, "capacity")
	b.Percent = int(pct)
	if pw, ok := s.readInt(d, "power_now"); ok {
		b.Watts = float64(pw) / 1e6
	} else if cur, ok := s.readInt(d, "current_now"); ok {
		v, _ := s.readInt(d, "voltage_now")
		b.Watts = float64(cur) / 1e6 * float64(v) / 1e6
	}
	wh := 0.0
	if en, ok := s.readInt(d, "energy_now"); ok {
		wh = float64(en) / 1e6
	} else if ch, ok := s.readInt(d, "charge_now"); ok {
		v, _ := s.readInt(d, "voltage_now")
		wh = float64(ch) / 1e6 * float64(v) / 1e6
	}
	if wh > 0 && b.Watts > 0.1 {
		b.HoursLeft = wh / b.Watts
	}
	return b
}

func (s Source) readGPUs() []GPU {
	devs, _ := filepath.Glob(s.p("sys/bus/pci/devices/*"))
	var out []GPU
	for _, d := range devs {
		rel := strings.TrimPrefix(d, s.Root)
		class, _ := s.read(rel, "class")
		if !strings.HasPrefix(class, "0x03") {
			continue
		}
		v, _ := s.read(rel, "vendor")
		g := GPU{Addr: filepath.Base(d)}
		switch v {
		case "0x10de":
			g.Vendor = "nvidia"
		case "0x1002":
			g.Vendor = "amd"
		case "0x8086":
			g.Vendor = "intel"
		default:
			g.Vendor = v
		}
		g.Runtime, _ = s.read(rel, "power/runtime_status")
		g.Control, _ = s.read(rel, "power/control")
		bv, _ := s.read(rel, "boot_vga")
		g.Boot = bv == "1"
		g.BusyPct = -1
		if g.Vendor == "nvidia" {
			if txt, ok := s.read("proc/driver/nvidia/gpus", g.Addr, "power"); ok {
				for _, line := range strings.Split(txt, "\n") {
					if v, found := strings.CutPrefix(line, "Runtime D3 status:"); found {
						g.NvidiaD3 = strings.TrimSpace(v)
					}
				}
			}
		}
		if m, _ := filepath.Glob(filepath.Join(d, "hwmon/hwmon*/power1_average")); len(m) > 0 {
			if uw, ok := s.readInt(strings.TrimPrefix(m[0], s.Root)); ok {
				g.Watts = float64(uw) / 1e6
			}
		}
		if n, ok := s.readInt(rel, "gpu_busy_percent"); ok {
			g.BusyPct = int(n)
		}
		out = append(out, g)
	}
	return out
}

// Rate is a process's activity over the sampling window.
type Rate struct {
	PID      int
	Comm     string
	CPU      float64 // percent of one core
	ReadBps  float64
	WriteBps float64
	RSSKiB   int64
	HoldsGPU bool
	HasIO    bool
	Wakeups  float64 // per second
}

// Report is what the rules look at.
type Report struct {
	Window   time.Duration
	Static   Static
	CPUBusy  float64 // 0..1 over the window
	PSI      map[string]PSI
	SwapPerS float64 // pages per second moved to or from swap
	MajPerS  float64
	Procs    []Rate // every process seen in both samples
}

func (r Report) top(n int, less func(a, b Rate) bool) []Rate {
	c := append([]Rate(nil), r.Procs...)
	sort.Slice(c, func(i, j int) bool { return less(c[i], c[j]) })
	if len(c) > n {
		c = c[:n]
	}
	return c
}

func (r Report) TopCPU(n int) []Rate {
	return r.top(n, func(a, b Rate) bool { return a.CPU > b.CPU })
}
func (r Report) TopIO(n int) []Rate {
	return r.top(n, func(a, b Rate) bool { return a.ReadBps+a.WriteBps > b.ReadBps+b.WriteBps })
}
func (r Report) TopMem(n int) []Rate {
	return r.top(n, func(a, b Rate) bool { return a.RSSKiB > b.RSSKiB })
}

// Diff turns two raw samples into a Report.
func Diff(a, b Raw, st Static) Report {
	w := b.At.Sub(a.At)
	if w <= 0 {
		w = time.Second
	}
	sec := w.Seconds()
	rep := Report{Window: w, Static: st, PSI: b.PSI}
	if dt := b.CPUTotal - a.CPUTotal; dt > 0 && b.CPUTotal >= a.CPUTotal {
		rep.CPUBusy = float64(b.CPUBusy-a.CPUBusy) / float64(dt)
	}
	if b.SwapPages >= a.SwapPages {
		rep.SwapPerS = float64(b.SwapPages-a.SwapPages) / sec
	}
	if b.MajFaults >= a.MajFaults {
		rep.MajPerS = float64(b.MajFaults-a.MajFaults) / sec
	}
	gpus := append([]GPU(nil), st.GPUs...)
	for i, g := range gpus {
		ia, ok1 := a.GPUIdleMs[g.Addr]
		ib, ok2 := b.GPUIdleMs[g.Addr]
		if ok1 && ok2 && ib >= ia {
			busy := 100 - float64(ib-ia)/(sec*1000)*100
			gpus[i].BusyPct = int(max(0, min(100, busy)) + 0.5)
		}
	}
	rep.Static.GPUs = gpus
	self := os.Getpid()
	for pid, pb := range b.Procs {
		if pid == self {
			continue // measuring must not show up as the cause
		}
		pa, ok := a.Procs[pid]
		if !ok || pa.Comm != pb.Comm {
			continue
		}
		r := Rate{PID: pid, Comm: pb.Comm, RSSKiB: pb.RSSKiB, HoldsGPU: pb.HoldsGPU, HasIO: pb.HasIO}
		if pb.Ticks >= pa.Ticks {
			r.CPU = float64(pb.Ticks-pa.Ticks) / clkTck / sec * 100
		}
		if pb.Wakeups >= pa.Wakeups {
			r.Wakeups = float64(pb.Wakeups-pa.Wakeups) / sec
		}
		if pb.HasIO && pa.HasIO {
			if pb.ReadB >= pa.ReadB {
				r.ReadBps = float64(pb.ReadB-pa.ReadB) / sec
			}
			if pb.WriteB >= pa.WriteB {
				r.WriteBps = float64(pb.WriteB-pa.WriteB) / sec
			}
		}
		rep.Procs = append(rep.Procs, r)
	}
	return rep
}

// Sample reads twice, window apart, and diffs.
func (s Source) Sample(window time.Duration) Report {
	a := s.readRaw()
	time.Sleep(window)
	b := s.readRaw()
	return Diff(a, b, s.readStatic())
}

// Group is every process with the same name added together, so forty
// copies of one program show up as one cause instead of forty small ones.
type Group struct {
	Comm    string
	Count   int
	CPU     float64
	PID     int // the busiest member
	Wakeups float64
	maxCPU  float64
}

func (r Report) GroupCPU(n int) []Group {
	m := map[string]*Group{}
	for _, p := range r.Procs {
		g := m[p.Comm]
		if g == nil {
			g = &Group{Comm: p.Comm}
			m[p.Comm] = g
		}
		g.Count++
		g.CPU += p.CPU
		g.Wakeups += p.Wakeups
		if p.CPU >= g.maxCPU {
			g.maxCPU, g.PID = p.CPU, p.PID
		}
	}
	out := make([]Group, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPU > out[j].CPU })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
