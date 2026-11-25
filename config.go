package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config is the optional ~/.config/kerna/config.json. Every key is optional;
// a missing or zero value keeps the built-in default.
type Config struct {
	Ignore     []string `json:"ignore,omitempty"`      // finding ids that are muted
	Notify     bool     `json:"notify,omitempty"`      // desktop notifications from the recording service
	StallWarn  float64  `json:"stall_warn,omitempty"`  // percent of the last 10 s stalled before kerna warns (default 10)
	StallBad   float64  `json:"stall_bad,omitempty"`   // ... before it calls it a problem (default 30)
	HotC       float64  `json:"hot_c,omitempty"`       // °C at which thermal throttling is suspected (default 88)
	DrainWatts float64  `json:"drain_watts,omitempty"` // battery draw that counts as fast (default 15)
}

func configPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "kerna", "config.json")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "kerna", "config.json")
}

// LoadConfig reads the config and applies the thresholds. A broken file is
// reported but never stops kerna: the defaults are used.
func LoadConfig() Config {
	var c Config
	b, err := os.ReadFile(configPath())
	if err != nil {
		applyThresholds(c)
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		fmt.Fprintln(os.Stderr, L("kerna: ignoring "+configPath()+": ", "kerna: şu dosya yok sayıldı: "+configPath()+": ")+err.Error())
		c = Config{}
	}
	applyThresholds(c)
	return c
}

func applyThresholds(c Config) {
	psiWarn, psiBad, hotC, drainWatts = 10.0, 30.0, 88.0, 15.0
	if c.StallWarn > 0 {
		psiWarn = c.StallWarn
	}
	if c.StallBad > 0 {
		psiBad = c.StallBad
	}
	if c.HotC > 0 {
		hotC = c.HotC
	}
	if c.DrainWatts > 0 {
		drainWatts = c.DrainWatts
	}
}

func SaveConfig(c Config) error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), append(b, '\n'), 0o644)
}

func knownIDs() []string {
	var ids []string
	for id := range titles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c Config) ignored(id string) bool {
	for _, x := range c.Ignore {
		if x == id {
			return true
		}
	}
	return false
}

// Filter drops muted findings and reports how many it dropped.
func (c Config) Filter(fs []Finding) (kept []Finding, muted int) {
	for _, f := range fs {
		if c.ignored(f.ID) {
			muted++
			continue
		}
		kept = append(kept, f)
	}
	return kept, muted
}

// Mute and Unmute change the list; they return false when nothing changed.
func (c *Config) Mute(id string) bool {
	if c.ignored(id) {
		return false
	}
	c.Ignore = append(c.Ignore, id)
	sort.Strings(c.Ignore)
	return true
}

func (c *Config) Unmute(id string) bool {
	for i, x := range c.Ignore {
		if x == id {
			c.Ignore = append(c.Ignore[:i], c.Ignore[i+1:]...)
			return true
		}
	}
	return false
}

func validID(id string) error {
	if _, ok := titles[id]; ok {
		return nil
	}
	return fmt.Errorf("%s: %q. %s", L("unknown finding", "bilinmeyen bulgu"), id, L("Known: ", "Bilinenler: ")+strings.Join(knownIDs(), ", "))
}

// ---- notifications ------------------------------------------------------

const (
	notifyAfter  = 4       // consecutive samples (one minute) before the first notification
	notifyRepeat = 30 * 60 // seconds before the same problem is announced again
)

// notifier decides when a problem has lasted long enough to be worth a
// desktop notification. It is pure so it can be tested without a desktop.
type notifier struct {
	streak map[string]int
	last   map[string]int64
}

func newNotifier() *notifier { return &notifier{streak: map[string]int{}, last: map[string]int64{}} }

func (n *notifier) observe(fs []Finding, nowUnix int64) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, f := range fs {
		if f.Sev < Warn {
			continue
		}
		seen[f.ID] = true
		n.streak[f.ID]++
		if n.streak[f.ID] >= notifyAfter {
			if last, ok := n.last[f.ID]; !ok || nowUnix-last >= notifyRepeat {
				n.last[f.ID] = nowUnix
				out = append(out, f)
			}
		}
	}
	for id := range n.streak {
		if !seen[id] {
			delete(n.streak, id)
		}
	}
	return out
}
