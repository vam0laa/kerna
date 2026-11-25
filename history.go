package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// History answers "what happened at 03:12?". A background `kerna record`
// appends one small line every few seconds to a file that stays on this
// machine and is pruned after a week.

const (
	recordEvery = 15 * time.Second
	keepFor     = 7 * 24 * time.Hour
)

// HFinding is a finding as stored. The title is not stored: it is looked up
// again in the reader's language.
type HFinding struct {
	ID       string   `json:"id"`
	Sev      Severity `json:"s"`
	Evidence []string `json:"e,omitempty"`
}

// HTop is one of the busiest programs of a sample.
type HTop struct {
	N string  `json:"n"`
	C float64 `json:"c"`
}

type Entry struct {
	T        time.Time  `json:"t"`
	CPU      float64    `json:"cpu"` // busy, 0..100
	PCPU     float64    `json:"pc"`  // stall percentages
	PIO      float64    `json:"pi"`
	PMem     float64    `json:"pm"`
	Temp     float64    `json:"temp,omitempty"` // °C
	Watts    float64    `json:"w,omitempty"`    // battery draw, only while discharging
	GPUWatts float64    `json:"gw,omitempty"`   // power draw reported by GPU drivers (amdgpu)
	Bat      int        `json:"b,omitempty"`    // battery percent
	Plugged  bool       `json:"pl,omitempty"`   // charging or full
	Top      []HTop     `json:"top,omitempty"`  // the busiest programs: name and percent of one core
	GTop     []HTop     `json:"gtop,omitempty"` // the programs that used the GPU most: name and percent of the GPU
	Findings []HFinding `json:"f,omitempty"`
}

var titles = map[string][2]string{
	"cpu-pressure":     {"Programs were waiting for the CPU", "Programlar işlemciyi bekledi"},
	"io-pressure":      {"Programs were waiting for the disk", "Programlar diski bekledi"},
	"memory-pressure":  {"Memory was running out", "Bellek tükeniyordu"},
	"thermal-throttle": {"The CPU slowed down because it was hot", "İşlemci ısındığı için yavaşladı"},
	"dgpu-awake":       {"A discrete GPU was awake", "Ayrık ekran kartı uyanıktı"},
	"gpu-load":         {"The GPU was working hard", "Ekran kartı yoğun çalışıyordu"},
	"gpu-busy":         {"A GPU was almost fully busy", "Bir ekran kartı neredeyse tamamen meşguldü"},
	"battery-drain":    {"The battery was draining fast", "Pil hızlı tükeniyordu"},
	"net-loss":         {"Network connections were losing packets", "Ağ bağlantıları paket kaybediyordu"},
	"wifi-weak":        {"The Wi-Fi signal was weak", "Wi-Fi sinyali zayıftı"},
	"power-profile":    {"A performance power profile was active on battery", "Pilde performans güç profili açıktı"},
}

func titleOf(id string) string {
	if t, ok := titles[id]; ok {
		return L(t[0], t[1])
	}
	return id
}

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "kerna")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", "kerna")
}

func historyPath() string { return filepath.Join(stateDir(), "history.jsonl") }

func entryOf(r Report, fs []Finding, now time.Time) Entry {
	e := Entry{T: now.Truncate(time.Second), CPU: round1(r.CPUBusy * 100), PCPU: r.PSI["cpu"].SomeAvg10,
		PIO: r.PSI["io"].SomeAvg10, PMem: r.PSI["memory"].SomeAvg10, Temp: round1(r.Static.MaxTempC)}
	if b := r.Static.Battery; b != nil {
		e.Bat = b.Percent
		e.Plugged = !strings.EqualFold(b.Status, "Discharging")
		if !e.Plugged {
			e.Watts = round1(b.Watts)
		}
	}
	for _, g := range r.Static.GPUs {
		e.GPUWatts += g.Watts
	}
	e.GPUWatts = round1(e.GPUWatts)
	for _, g := range r.GroupCPU(3) {
		if g.CPU >= 1 {
			e.Top = append(e.Top, HTop{N: g.Comm, C: round1(g.CPU)})
		}
	}
	for _, g := range r.TopGPU(3) {
		if g.GPU >= 3 {
			e.GTop = append(e.GTop, HTop{N: g.Comm, C: round1(g.GPU)})
		}
	}
	for _, f := range fs {
		e.Findings = append(e.Findings, HFinding{ID: f.ID, Sev: f.Sev, Evidence: f.Evidence})
	}
	return e
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func appendEntry(e Entry) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

func readHistory(path string, since time.Time) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.T.Before(since) {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// prune rewrites the file without entries older than keepFor.
func prune(path string, now time.Time) error {
	es, err := readHistory(path, now.Add(-keepFor))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range es {
		b, _ := json.Marshal(e)
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

// record samples forever. Each sample is cheap: a 1 s window every recordEvery.
func record(src Source, every time.Duration) {
	last := time.Time{}
	nt := newNotifier()
	for {
		rep := src.Sample(time.Second)
		now := time.Now()
		cfg := LoadConfig() // read every round, so `kerna notify on` and `kerna ignore` need no restart
		fs, _ := cfg.Filter(Diagnose(rep))
		if cfg.Notify {
			for _, f := range nt.observe(fs, now.Unix()) {
				sendNotification(f)
			}
		}
		if err := appendEntry(entryOf(rep, fs, now)); err != nil {
			fmt.Fprintln(os.Stderr, "kerna: cannot write history:", err)
		}
		if now.Sub(last) > time.Hour {
			prune(historyPath(), now)
			last = now
		}
		time.Sleep(every - time.Second)
	}
}

// Incident is a run of samples in which the same problem was present.
type Incident struct {
	ID       string
	From, To time.Time
	Sev      Severity
	Peak     string
	Evidence string
	Samples  int
}

func peakOf(id string, e Entry) float64 {
	switch id {
	case "cpu-pressure":
		return e.PCPU
	case "io-pressure":
		return e.PIO
	case "memory-pressure":
		return e.PMem
	case "thermal-throttle":
		return e.Temp
	case "battery-drain":
		return e.Watts
	}
	return 0
}

func peakText(id string, v float64) string {
	switch id {
	case "cpu-pressure", "io-pressure", "memory-pressure":
		return L(fmt.Sprintf("peak stall %.0f%%", v), fmt.Sprintf("en yüksek bekleme %%%.0f", v))
	case "thermal-throttle":
		return fmt.Sprintf("%.0f °C", v)
	case "battery-drain":
		return fmt.Sprintf("%.0f W", v)
	}
	return ""
}

// Incidents groups entries. A gap longer than 3 intervals ends an incident.
func Incidents(es []Entry) []Incident {
	open := map[string]*Incident{}
	peaks := map[string]float64{}
	var done []Incident
	closeInc := func(id string) {
		in := open[id]
		in.Peak = peakText(id, peaks[id])
		done = append(done, *in)
		delete(open, id)
	}
	var prev time.Time
	for _, e := range es {
		if !prev.IsZero() && e.T.Sub(prev) > 3*recordEvery {
			for id := range open {
				closeInc(id)
			}
		}
		prev = e.T
		seen := map[string]bool{}
		for _, f := range e.Findings {
			seen[f.ID] = true
			in := open[f.ID]
			if in == nil {
				ev := ""
				if len(f.Evidence) > 0 {
					ev = f.Evidence[0]
				}
				in = &Incident{ID: f.ID, From: e.T, Evidence: ev}
				open[f.ID] = in
				peaks[f.ID] = 0
			}
			in.To, in.Samples = e.T, in.Samples+1
			if f.Sev > in.Sev {
				in.Sev = f.Sev
			}
			if v := peakOf(f.ID, e); v > peaks[f.ID] {
				peaks[f.ID] = v
			}
		}
		for id := range open {
			if !seen[id] {
				closeInc(id)
			}
		}
	}
	for id := range open {
		closeInc(id)
	}
	sort.Slice(done, func(i, j int) bool { return done[i].From.Before(done[j].From) })
	return done
}

// nearest returns the entry closest to t, if one is within 2 minutes.
func nearest(es []Entry, t time.Time) (Entry, bool) {
	best, bd := Entry{}, 2*time.Minute+1
	for _, e := range es {
		d := e.T.Sub(t)
		if d < 0 {
			d = -d
		}
		if d < bd {
			best, bd = e, d
		}
	}
	return best, bd <= 2*time.Minute
}

// parseAt reads "15:04" (today, or yesterday if that is still in the future) or "2006-01-02 15:04".
func parseAt(s string, now time.Time) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, now.Location()); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("15:04", s, now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("time must look like 03:12 or 2026-11-25 03:12")
	}
	t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t, nil
}

// sendNotification shows a desktop notification through notify-send, if it is installed.
var sendNotification = func(f Finding) {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	body := f.Why
	if len(f.Evidence) > 0 {
		body += "\n" + f.Evidence[0]
	}
	urgency := "normal"
	if f.Sev == Bad {
		urgency = "critical"
	}
	exec.Command(path, "-a", "kerna", "-u", urgency, "kerna: "+f.Title, body).Run()
}
