package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// `kerna why <name or pid>` looks closely at one program: how long it has been
// running, what it uses right now compared with its whole life, which of its
// threads do the work, its GPU time and its child programs.

type threadUse struct {
	Name string
	CPU  float64 // percent of one core
}

type childUse struct {
	Comm string
	PID  int
	CPU  float64
}

type WhyReport struct {
	Query     string
	PIDs      []int
	Comm      string
	Cmdline   string
	Parent    string
	Age       time.Duration
	LifeCPU   float64 // average CPU over its whole life, percent of one core
	NowCPU    float64 // over the sampling window
	Threads   []threadUse
	NumThread int
	GPU       float64
	WakeupsPS float64
	RSSKiB    int64
	Children  []childUse
	Window    time.Duration
}

type procSnap struct {
	pid      int
	comm     string
	ticks    uint64
	start    uint64 // clock ticks after boot
	rssKiB   int64
	gpuNs    uint64
	wake     uint64
	threads  map[string]uint64 // thread name -> ticks (summed)
	nthreads int
}

func (s Source) uptime() float64 {
	t, _ := s.read("proc/uptime")
	f, _ := strconv.ParseFloat(strings.Fields(t + " 0")[0], 64)
	return f
}

func (s Source) snapProc(pid int) (procSnap, bool) {
	d := strconv.Itoa(pid)
	b, err := os.ReadFile(s.p("proc", d, "stat"))
	if err != nil {
		return procSnap{}, false
	}
	pr, ok := parseStatLine(string(b))
	if !ok {
		return procSnap{}, false
	}
	line := string(b)
	f := strings.Fields(line[strings.LastIndexByte(line, ')')+1:])
	var start uint64
	if len(f) > 19 {
		start, _ = strconv.ParseUint(f[19], 10, 64) // field 22
	}
	ps := procSnap{pid: pid, comm: pr.Comm, ticks: pr.Ticks, start: start, rssKiB: pr.RSSKiB, threads: map[string]uint64{}}
	if t, ok := s.read("proc", d, "status"); ok {
		for _, l := range strings.Split(t, "\n") {
			if v, found := strings.CutPrefix(l, "voluntary_ctxt_switches:"); found {
				ps.wake, _ = strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			}
		}
	}
	tasks, _ := os.ReadDir(s.p("proc", d, "task"))
	ps.nthreads = len(tasks)
	for _, t := range tasks {
		tb, err := os.ReadFile(s.p("proc", d, "task", t.Name(), "stat"))
		if err != nil {
			continue
		}
		tp, ok := parseStatLine(string(tb))
		if !ok {
			continue
		}
		name := tp.Comm
		if c, ok := s.read("proc", d, "task", t.Name(), "comm"); ok && c != "" {
			name = c
		}
		ps.threads[name] += tp.Ticks
	}
	if fds, err := os.ReadDir(s.p("proc", d, "fd")); err == nil {
		seen := map[string]bool{}
		for _, fd := range fds {
			dst, err := os.Readlink(s.p("proc", d, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(dst, "/dev/dri/") {
				continue
			}
			if txt, ok := s.read("proc", d, "fdinfo", fd.Name()); ok {
				if id, ns, ok := parseDRMFdinfo(txt); ok && !seen[id] {
					seen[id] = true
					ps.gpuNs += ns
				}
			}
		}
	}
	return ps, true
}

// findProcs resolves a pid or a program name (exact first, then part of the name).
func (s Source) findProcs(query string) []int {
	if n, err := strconv.Atoi(query); err == nil {
		if _, ok := s.snapProc(n); ok {
			return []int{n}
		}
		return nil
	}
	entries, _ := os.ReadDir(s.p("proc"))
	var exact, partial []int
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		c, ok := s.read("proc", e.Name(), "comm")
		if !ok {
			continue
		}
		switch {
		case c == query:
			exact = append(exact, pid)
		case strings.Contains(strings.ToLower(c), strings.ToLower(query)):
			partial = append(partial, pid)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

func (s Source) children(pids []int) []int {
	set := map[int]bool{}
	for _, p := range pids {
		set[p] = true
	}
	var out []int
	entries, _ := os.ReadDir(s.p("proc"))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || set[pid] {
			continue
		}
		b, err := os.ReadFile(s.p("proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		line := string(b)
		f := strings.Fields(line[strings.LastIndexByte(line, ')')+1:])
		if len(f) > 1 {
			if pp, _ := strconv.Atoi(f[1]); set[pp] {
				out = append(out, pid)
			}
		}
	}
	return out
}

// Why measures the program over the window.
func (s Source) Why(query string, window time.Duration) (WhyReport, bool) {
	pids := s.findProcs(query)
	if len(pids) == 0 {
		return WhyReport{}, false
	}
	rep := WhyReport{Query: query, PIDs: pids, Window: window}
	kids := s.children(pids)
	snap := func(list []int) map[int]procSnap {
		m := map[int]procSnap{}
		for _, p := range list {
			if ps, ok := s.snapProc(p); ok {
				m[p] = ps
			}
		}
		return m
	}
	t0 := time.Now()
	a, ka := snap(pids), snap(kids)
	time.Sleep(window)
	b, kb := snap(pids), snap(kids)
	sec := time.Since(t0).Seconds()
	up := s.uptime()

	threads := map[string]float64{}
	var life, lifeCPUSeconds float64
	for pid, pb := range b {
		pa, ok := a[pid]
		if !ok {
			continue
		}
		rep.Comm = pb.comm
		rep.RSSKiB += pb.rssKiB
		rep.NumThread += pb.nthreads
		rep.NowCPU += float64(pb.ticks-pa.ticks) / clkTck / sec * 100
		rep.GPU += float64(pb.gpuNs-pa.gpuNs) / (sec * 1e9) * 100
		rep.WakeupsPS += float64(pb.wake-pa.wake) / sec
		for n, t := range pb.threads {
			if t >= pa.threads[n] {
				threads[n] += float64(t-pa.threads[n]) / clkTck / sec * 100
			}
		}
		age := up - float64(pb.start)/clkTck
		if age > 1 {
			lifeCPUSeconds += float64(pb.ticks) / clkTck
			if age > life {
				life = age
			}
		}
	}
	rep.Age = time.Duration(life * float64(time.Second))
	if life > 1 {
		rep.LifeCPU = lifeCPUSeconds / life * 100
	}
	for n, v := range threads {
		rep.Threads = append(rep.Threads, threadUse{n, v})
	}
	sort.Slice(rep.Threads, func(i, j int) bool { return rep.Threads[i].CPU > rep.Threads[j].CPU })
	if len(rep.Threads) > 5 {
		rep.Threads = rep.Threads[:5]
	}
	for pid, kbp := range kb {
		if ka2, ok := ka[pid]; ok {
			rep.Children = append(rep.Children, childUse{kbp.comm, pid, float64(kbp.ticks-ka2.ticks) / clkTck / sec * 100})
		}
	}
	sort.Slice(rep.Children, func(i, j int) bool { return rep.Children[i].CPU > rep.Children[j].CPU })
	if cl, ok := s.read("proc", strconv.Itoa(pids[0]), "cmdline"); ok {
		rep.Cmdline = strings.TrimSpace(strings.ReplaceAll(cl, "\x00", " "))
	}
	if b, err := os.ReadFile(s.p("proc", strconv.Itoa(pids[0]), "stat")); err == nil {
		line := string(b)
		f := strings.Fields(line[strings.LastIndexByte(line, ')')+1:])
		if len(f) > 1 {
			if pp, err := strconv.Atoi(f[1]); err == nil {
				rep.Parent, _ = s.read("proc", strconv.Itoa(pp), "comm")
			}
		}
	}
	return rep, true
}

var renderLike = []string{"render", "gl", "compositor", "paint", "draw", "raster"}

func looksLikeRendering(thread string) bool {
	l := strings.ToLower(thread)
	for _, k := range renderLike {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func durationText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return L(fmt.Sprintf("%d days", int(d.Hours()/24)), fmt.Sprintf("%d gün", int(d.Hours()/24)))
	case d >= time.Hour:
		return L(fmt.Sprintf("%dh %dmin", int(d.Hours()), int(d.Minutes())%60), fmt.Sprintf("%d sa %d dk", int(d.Hours()), int(d.Minutes())%60))
	case d >= time.Minute:
		return L(fmt.Sprintf("%d min", int(d.Minutes())), fmt.Sprintf("%d dk", int(d.Minutes())))
	}
	return L(fmt.Sprintf("%d s", int(d.Seconds())), fmt.Sprintf("%d sn", int(d.Seconds())))
}

// Observations are the plain-language reading of the numbers. They describe
// what the numbers show; they do not claim to know the cause.
func (w WhyReport) Observations() []string {
	var out []string
	if w.LifeCPU >= 20 && w.LifeCPU >= 2*w.NowCPU && w.NowCPU < w.LifeCPU-10 {
		out = append(out, L(fmt.Sprintf("Its average over its whole life is %s%%, much higher than the %s%% right now: it was busier earlier, so judge the current number.", fl(w.LifeCPU, 0), fl(w.NowCPU, 0)),
			fmt.Sprintf("Tüm ömrü boyunca ortalaması %%%s, şu andaki %%%s'den çok yüksek: daha önce daha yoğundu, bu yüzden şimdiki sayıya bak.", fl(w.LifeCPU, 0), fl(w.NowCPU, 0))))
	}
	if len(w.Threads) > 0 && w.NowCPU >= 10 && w.Threads[0].CPU >= 0.4*w.NowCPU && looksLikeRendering(w.Threads[0].Name) {
		out = append(out, L(fmt.Sprintf("Most of its work is in the thread %q: it keeps redrawing the screen. That is typical for animations, visualizers or a widget that updates all the time.", w.Threads[0].Name),
			fmt.Sprintf("İşinin çoğu %q iş parçacığında: ekranı sürekli yeniden çiziyor. Bu, animasyonlarda, görselleştiricilerde ya da sürekli güncellenen bir widget'ta tipiktir.", w.Threads[0].Name)))
	}
	if w.GPU >= 10 {
		out = append(out, L(fmt.Sprintf("It also keeps the GPU busy: %s%% of its time.", fl(w.GPU, 0)), fmt.Sprintf("Ekran kartını da meşgul ediyor: zamanının %%%s'i.", fl(w.GPU, 0))))
	}
	for _, c := range w.Children {
		if c.CPU >= 3 {
			out = append(out, L(fmt.Sprintf("It runs %s (pid %d) as a child, which uses %s%% of a core.", c.Comm, c.PID, fl(c.CPU, 0)), fmt.Sprintf("Alt süreç olarak %s (pid %d) çalıştırıyor, o da bir çekirdeğin %%%s'ini kullanıyor.", c.Comm, c.PID, fl(c.CPU, 0))))
		}
	}
	if w.WakeupsPS >= 500 {
		out = append(out, L(fmt.Sprintf("It wakes up %s times a second, which keeps the CPU from sleeping deeply.", fl(w.WakeupsPS, 0)), fmt.Sprintf("Saniyede %s kez uyanıyor; bu CPU'nun derin uykuya geçmesini engeller.", fl(w.WakeupsPS, 0))))
	}
	if len(out) == 0 {
		out = append(out, L(fmt.Sprintf("Nothing unusual: %s%% of a core right now.", fl(w.NowCPU, 0)), fmt.Sprintf("Olağandışı bir şey yok: şu an bir çekirdeğin %%%s'i.", fl(w.NowCPU, 0))))
	}
	return out
}

func WhyLines(p palette, w WhyReport) []string {
	title := w.Comm
	if d := describe(w.Comm); d != "" {
		title += p.c(cDim, " · "+d)
	}
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("why", "neden")+" ") + p.c(cBold, title), ""}
	procs := L(fmt.Sprintf("%d process", len(w.PIDs)), fmt.Sprintf("%d süreç", len(w.PIDs)))
	if len(w.PIDs) > 1 {
		procs = L(fmt.Sprintf("%d processes", len(w.PIDs)), fmt.Sprintf("%d süreç", len(w.PIDs)))
	} else {
		procs += fmt.Sprintf(" (pid %d)", w.PIDs[0])
	}
	out = append(out, "  "+p.c(cDim, procs+", "+L("running for ", "çalışma süresi ")+durationText(w.Age)+", "+L(fmt.Sprintf("%d threads", w.NumThread), fmt.Sprintf("%d iş parçacığı", w.NumThread))+", RAM "+mib(w.RSSKiB)))
	if w.Cmdline != "" {
		out = append(out, "  "+p.c(cDim, clip(w.Cmdline, 90)))
	}
	if w.Parent != "" {
		out = append(out, "  "+p.c(cDim, L("started by ", "başlatan: ")+w.Parent))
	}
	out = append(out, "")
	out = append(out, "  "+p.c(cBold, L("CPU", "CPU"))+fmt.Sprintf("   %s%% %s   ·   %s%% %s", fl(w.NowCPU, 0), L("now", "şimdi"), fl(w.LifeCPU, 0), L("average over its whole life", "tüm ömrü boyunca ortalama")))
	if w.GPU >= 1 {
		out = append(out, "  "+p.c(cBold, "GPU")+fmt.Sprintf("   %s%% %s", fl(w.GPU, 0), L("of the GPU now", "GPU'nun şu an")))
	}
	if w.WakeupsPS >= 1 {
		out = append(out, "  "+p.c(cBold, L("Wake-ups", "Uyanma"))+fmt.Sprintf("   %s/%s", fl(w.WakeupsPS, 0), L("s", "sn")))
	}
	if len(w.Threads) > 0 {
		out = append(out, "", "  "+p.c(cBold, L("Busiest threads", "En yoğun iş parçacıkları")))
		for _, t := range w.Threads {
			if t.CPU < 0.5 {
				continue
			}
			out = append(out, fmt.Sprintf("    %-24s %s%%", clip(t.Name, 24), fl(t.CPU, 0)))
		}
	}
	var kids []string
	for _, c := range w.Children {
		if c.CPU >= 0.5 {
			kids = append(kids, fmt.Sprintf("%s %s%%", c.Comm, fl(c.CPU, 0)))
		}
	}
	if len(kids) > 0 {
		out = append(out, "", "  "+p.c(cBold, L("Child programs", "Alt programlar"))+"   "+strings.Join(kids, "  ·  "))
	}
	out = append(out, "")
	for _, o := range w.Observations() {
		out = append(out, "  "+p.c(cAccent, "› ")+strings.Join(wrap(o, 76), "\n    "))
	}
	return out
}
