package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Snap is a saved measurement, for "did my change help?".
type Snap struct {
	T        time.Time         `json:"t"`
	CPU      float64           `json:"cpu"`
	PCPU     float64           `json:"pc"`
	PIO      float64           `json:"pi"`
	PMem     float64           `json:"pm"`
	MemUsed  float64           `json:"mem"`
	Temp     float64           `json:"temp"`
	Watts    float64           `json:"w"`
	GPUWatts float64           `json:"gw"`
	GPUState map[string]string `json:"gpu,omitempty"`
	Profile  string            `json:"profile,omitempty"`
	Findings []string          `json:"f,omitempty"`
}

func snapOf(r Report, fs []Finding, now time.Time) Snap {
	st := r.Static
	s := Snap{T: now.Truncate(time.Second), CPU: round1(r.CPUBusy * 100), PCPU: r.PSI["cpu"].SomeAvg10,
		PIO: r.PSI["io"].SomeAvg10, PMem: r.PSI["memory"].SomeAvg10, Temp: round1(st.MaxTempC), GPUState: map[string]string{}, Profile: st.Profile}
	if st.MemTotalKiB > 0 {
		s.MemUsed = round1(100 - float64(st.MemAvailKiB)/float64(st.MemTotalKiB)*100)
	}
	if st.Battery != nil && strings.EqualFold(st.Battery.Status, "Discharging") {
		s.Watts = round1(st.Battery.Watts)
	}
	for _, g := range st.GPUs {
		s.GPUWatts += g.Watts
		if g.Runtime != "" {
			s.GPUState[gpuName(g.Vendor)] = g.Runtime
		}
	}
	s.GPUWatts = round1(s.GPUWatts)
	for _, f := range fs {
		s.Findings = append(s.Findings, f.ID)
	}
	return s
}

var snapName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func snapPath(name string) string { return filepath.Join(stateDir(), "snapshots", name+".json") }

func SaveSnap(name string, s Snap) error {
	if !snapName.MatchString(name) {
		return fmt.Errorf("%s", L("the name may use a-z, 0-9, - and _ (max 32 characters)", "ad yalnızca a-z, 0-9, - ve _ içerebilir (en çok 32 karakter)"))
	}
	if err := os.MkdirAll(filepath.Dir(snapPath(name)), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return os.WriteFile(snapPath(name), b, 0o600)
}

func LoadSnap(name string) (Snap, error) {
	var s Snap
	if !snapName.MatchString(name) {
		return s, fmt.Errorf("%s", L("bad snapshot name", "geçersiz anlık görüntü adı"))
	}
	b, err := os.ReadFile(snapPath(name))
	if err != nil {
		return s, fmt.Errorf("%s", L("no snapshot named "+name+". Save one first: kerna snapshot "+name, name+" adlı anlık görüntü yok. Önce kaydet: kerna snapshot "+name))
	}
	return s, json.Unmarshal(b, &s)
}

type row struct {
	label         string
	before, after float64
	unit          string
	minDelta      float64 // changes smaller than this are "the same": measurements wobble
}

// CompareLines shows what changed between a saved snapshot and now.
// For every number here, lower is better.
func CompareLines(p palette, a, b Snap) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("compare", "karşılaştırma")), ""}
	out = append(out, p.c(cDim, fmt.Sprintf("  %-14s%-12s%s", "", a.T.Format("15:04:05"), L("now", "şimdi"))))
	rows := []row{
		{L("Battery draw", "Pil çekimi"), a.Watts, b.Watts, " W", 0.5},
		{L("GPU draw", "GPU çekimi"), a.GPUWatts, b.GPUWatts, " W", 0.5},
		{L("Temperature", "Sıcaklık"), a.Temp, b.Temp, " °C", 2},
		{L("Processor", "İşlemci"), a.CPU, b.CPU, "%", 3},
		{L("CPU wait", "CPU bekleme"), a.PCPU, b.PCPU, "%", 3},
		{L("Disk wait", "Disk bekleme"), a.PIO, b.PIO, "%", 3},
		{L("Memory used", "Bellek dolu"), a.MemUsed, b.MemUsed, "%", 3},
	}
	for _, r := range rows {
		if r.before == 0 && r.after == 0 {
			continue
		}
		d := r.after - r.before
		verdict := p.c(cDim, L("same", "aynı"))
		switch {
		case d <= -r.minDelta:
			verdict = p.c(cOK, fmt.Sprintf("↓ %s%s %s", fl(-d, 1), r.unit, L("better", "daha iyi")))
		case d >= r.minDelta:
			verdict = p.c(cBad, fmt.Sprintf("↑ %s%s %s", fl(d, 1), r.unit, L("worse", "daha kötü")))
		}
		out = append(out, fmt.Sprintf("  %-14s%-12s%-12s%s", r.label, fl(r.before, 1)+r.unit, fl(r.after, 1)+r.unit, verdict))
	}
	var names []string
	for k := range a.GPUState {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if b.GPUState[k] != "" && a.GPUState[k] != b.GPUState[k] {
			good := b.GPUState[k] == "suspended"
			v := p.c(cWarn, b.GPUState[k])
			if good {
				v = p.c(cOK, b.GPUState[k]+" ✔")
			}
			out = append(out, fmt.Sprintf("  %-14s%-12s%-12s%s", k+" GPU", a.GPUState[k], b.GPUState[k], v))
		}
	}
	if a.Profile != b.Profile && b.Profile != "" {
		out = append(out, fmt.Sprintf("  %-14s%-12s%-12s", L("Power profile", "Güç profili"), a.Profile, b.Profile))
	}
	out = append(out, "")
	fixed, added := diffIDs(a.Findings, b.Findings)
	for _, id := range fixed {
		out = append(out, "  "+p.c(cOK, "✔ ")+L("gone: ", "çözüldü: ")+titleOf(id))
	}
	for _, id := range added {
		out = append(out, "  "+p.c(cBad, "✖ ")+L("new: ", "yeni: ")+titleOf(id))
	}
	if len(fixed) == 0 && len(added) == 0 {
		out = append(out, p.c(cDim, "  "+L("The findings are the same as before.", "Bulgular öncekiyle aynı.")))
	}
	out = append(out, "", p.c(cDim, "  "+L("A single sample wobbles: repeat it a few times before you trust a small change.", "Tek bir örnek oynak olur: küçük bir değişikliğe güvenmeden önce birkaç kez tekrarla.")))
	return out
}

func diffIDs(before, after []string) (gone, added []string) {
	in := func(s []string, x string) bool {
		for _, y := range s {
			if y == x {
				return true
			}
		}
		return false
	}
	for _, x := range before {
		if !in(after, x) {
			gone = append(gone, x)
		}
	}
	for _, x := range after {
		if !in(before, x) {
			added = append(added, x)
		}
	}
	return
}
