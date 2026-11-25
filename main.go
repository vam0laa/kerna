// kerna tells you why your Linux system is slow, hot or draining its battery,
// not just what the numbers are. It only reads /proc and /sys, needs no root,
// changes nothing and sends nothing anywhere.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, L(`kerna: why is my system slow, hot or draining the battery?

  kerna                    sample for a moment and explain what is wrong
  kerna watch              live view: q quit, p pause, h history
  kerna --json             the same diagnosis as JSON, for scripts and status bars
  kerna history            problems in the last 24 hours (--since 3d for longer)
  kerna history --at 03:12 what was going on at that moment
  kerna history --today    a summary of today: battery, busiest programs, problems
  kerna sleep              suspend problems and battery drain while asleep
  kerna why <name|pid>     look closely at one program: now vs its whole life, threads, GPU, children
  kerna check              what kerna can and cannot see on this machine (paste it into bug reports)
  kerna completion fish    shell completion (fish, bash or zsh)
  kerna service install    record history in the background (a systemd user service)
  kerna service uninstall  stop recording
  kerna why-froze          why did the last session end badly? (reads the system log with journalctl)
  kerna snapshot [name]    save a measurement, then change something...
  kerna compare [name]     ...and see what got better or worse
  kerna waybar             one JSON line for a Waybar custom module
  kerna ignore [id]        mute a finding (no id: list); kerna unignore <id> brings it back
  kerna notify on|off      desktop notifications from the recording service
  kerna version

Read-only: it reads /proc and /sys, needs no root and never runs the commands it suggests.
The only command that runs another program is why-froze (journalctl, read-only).
Settings: ~/.config/kerna/config.json (optional).
History stays in ~/.local/state/kerna on this machine and is deleted after 7 days.
`, `kerna: sistem neden yavaş, sıcak ya da pili tüketiyor?

  kerna                    bir an örnekle ve neyin yanlış olduğunu açıkla
  kerna watch              canlı görünüm: q çık, p duraklat, h geçmiş
  kerna --json             aynı teşhisi betikler ve durum çubukları için JSON olarak ver
  kerna history            son 24 saatteki sorunlar (daha uzun için --since 3d)
  kerna history --at 03:12 o anda neler oluyordu
  kerna history --today    bugünün özeti: pil, en yoğun programlar, sorunlar
  kerna sleep              uyku sorunları ve uykudayken pil tüketimi
  kerna why <ad|pid>       tek bir programa yakından bak: şimdi ve tüm ömrü, iş parçacıkları, GPU, alt programlar
  kerna check              kerna bu makinede neyi görebiliyor, neyi göremiyor (hata bildirimine yapıştır)
  kerna completion fish    kabuk tamamlama (fish, bash ya da zsh)
  kerna service install    geçmişi arka planda kaydet (bir systemd kullanıcı servisi)
  kerna service uninstall  kaydı durdur
  kerna why-froze          son oturum neden kötü bitti? (sistem günlüğünü journalctl ile okur)
  kerna snapshot [ad]      bir ölçüm kaydet, sonra bir şeyi değiştir...
  kerna compare [ad]       ...ve neyin iyileştiğini ya da kötüleştiğini gör
  kerna waybar             Waybar özel modülü için tek satır JSON
  kerna ignore [id]        bir bulguyu sustur (id yoksa liste); kerna unignore <id> geri getirir
  kerna notify on|off      kayıt servisinden masaüstü bildirimleri
  kerna version

Salt okunur: /proc ve /sys dosyalarını okur, root gerektirmez ve önerdiği komutları asla çalıştırmaz.
Başka bir program çalıştıran tek komut why-froze (journalctl, salt okunur).
Ayarlar: ~/.config/kerna/config.json (isteğe bağlı).
Geçmiş bu makinede ~/.local/state/kerna içinde durur ve 7 gün sonra silinir.
`))
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if cmd == "service" && len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("kerna", flag.ExitOnError)
	fs.Usage = usage
	asJSON := fs.Bool("json", false, "print JSON")
	secs := fs.Float64("n", 2, "sampling window in seconds")
	since := fs.Duration("since", 24*time.Hour, "history: how far back to look")
	at := fs.String("at", "", "history: show one moment, e.g. 03:12")
	today := fs.Bool("today", false, "history: a summary of today")
	// flags may come before or after the positional words: kerna why qs -n 3
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}

	src := Source{Root: "/"}
	if r := os.Getenv("KERNA_ROOT"); r != "" {
		src.Root = r // a copy of another machine's /proc and /sys, and for tests
	}
	win := time.Duration(*secs * float64(time.Second))
	if win < 500*time.Millisecond {
		win = 500 * time.Millisecond
	}
	pal := palette{useColor(os.Stdout)}

	cfg := LoadConfig()
	switch cmd {
	case "ignore", "unignore", "notify":
		configCmd(cmd, pos)
	case "why-froze":
		whyFroze(pal)
	case "snapshot":
		name := "last"
		if len(pos) > 0 {
			name = pos[0]
		}
		rep := src.Sample(win)
		kept, _ := cfg.Filter(Diagnose(rep))
		if err := SaveSnap(name, snapOf(rep, kept, time.Now())); err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(2)
		}
		fmt.Println(L("Saved. Change something, then run: kerna compare "+name, "Kaydedildi. Bir şeyi değiştir, sonra çalıştır: kerna compare "+name))
	case "compare":
		name := "last"
		if len(pos) > 0 {
			name = pos[0]
		}
		before, err := LoadSnap(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(1)
		}
		rep := src.Sample(win)
		kept, _ := cfg.Filter(Diagnose(rep))
		for _, l := range CompareLines(pal, before, snapOf(rep, kept, time.Now())) {
			fmt.Println(l)
		}
	case "waybar":
		rep := src.Sample(time.Second)
		kept, _ := cfg.Filter(Diagnose(rep))
		fmt.Println(Waybar(kept))
	case "version":
		fmt.Println("kerna", version)
	case "watch":
		watch(src, time.Second)
	case "record":
		record(src, recordEvery)
	case "service":
		service(sub)
	case "history":
		if *today {
			es, _ := readHistory(historyPath(), time.Now().Add(-24*time.Hour))
			for _, l := range TodayLines(pal, es, time.Now()) {
				fmt.Println(l)
			}
			return
		}
		showHistory(pal, *since, *at)
	case "check":
		for _, l := range CheckLines(pal, src.Check()) {
			fmt.Println(l)
		}
	case "completion":
		if len(pos) != 1 {
			fmt.Fprintln(os.Stderr, L("usage: kerna completion fish|bash|zsh", "kullanım: kerna completion fish|bash|zsh"))
			os.Exit(2)
		}
		s, err := Completion(pos[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(2)
		}
		fmt.Print(s)
	case "why":
		if len(pos) != 1 {
			usage()
			os.Exit(2)
		}
		w, ok := src.Why(pos[0], win)
		if !ok {
			fmt.Fprintln(os.Stderr, L("kerna: no running program matches \""+pos[0]+"\"", "kerna: \""+pos[0]+"\" ile eşleşen çalışan program yok"))
			os.Exit(1)
		}
		for _, l := range WhyLines(pal, w) {
			fmt.Println(l)
		}
	case "sleep":
		es, _ := readHistory(historyPath(), time.Now().Add(-keepFor))
		for _, l := range SleepLines(pal, src.readSleep(), es) {
			fmt.Println(l)
		}
	case "", "diagnose":
		rep := src.Sample(win)
		fnds, muted := cfg.Filter(Diagnose(rep))
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(struct {
				Findings []Finding `json:"findings"`
			}{append([]Finding{}, fnds...)})
			return
		}
		host, _ := os.Hostname()
		for _, l := range Frame(pal, rep, fnds, &Hist{}, 80, host, time.Now(), "", false) {
			fmt.Println(l)
		}
		if muted > 0 {
			fmt.Println(pal.c(cDim, "  "+L(fmt.Sprintf("%d muted finding(s): kerna ignore", muted), fmt.Sprintf("%d susturulmuş bulgu: kerna ignore", muted))))
		}
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "kerna: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func showHistory(pal palette, since time.Duration, at string) {
	now := time.Now()
	if at != "" {
		t, err := parseAt(at, now)
		if err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(2)
		}
		es, _ := readHistory(historyPath(), t.Add(-time.Hour))
		e, ok := nearest(es, t)
		if !ok {
			fmt.Println(L("Nothing was recorded within 2 minutes of that time. (Recording runs only after: kerna service install)", "O saatin 2 dakika yakınında kayıt yok. (Kayıt yalnızca şundan sonra çalışır: kerna service install)"))
			os.Exit(1)
		}
		for _, l := range MomentLines(pal, e, 80) {
			fmt.Println(l)
		}
		return
	}
	es, err := readHistory(historyPath(), now.Add(-since))
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "kerna:", err)
		os.Exit(1)
	}
	for _, l := range HistoryLines(pal, es, since, 80) {
		fmt.Println(l)
	}
}

const unitName = "kerna.service"

func unitPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "systemd", "user", unitName)
}

func service(sub string) {
	run := func(a ...string) error {
		c := exec.Command("systemctl", append([]string{"--user"}, a...)...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	}
	switch sub {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(1)
		}
		unit := "[Unit]\nDescription=kerna: record system history\n\n[Service]\nExecStart=" + exe + " record\nNice=19\nIOSchedulingClass=idle\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
		if err := os.MkdirAll(filepath.Dir(unitPath()), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(unitPath(), []byte(unit), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(1)
		}
		run("daemon-reload")
		if err := run("enable", "--now", unitName); err != nil {
			fmt.Fprintln(os.Stderr, "kerna: could not start the service:", err)
			os.Exit(1)
		}
		fmt.Println(L("Recording started. Later: kerna history", "Kayıt başladı. Sonra bak: kerna history"))
	case "uninstall":
		run("disable", "--now", unitName)
		os.Remove(unitPath())
		run("daemon-reload")
		fmt.Println(L("Recording stopped. Your history stays in "+stateDir()+" until you delete it.", "Kayıt durdu. Geçmişin sen silene kadar "+stateDir()+" içinde kalır."))
	default:
		usage()
		os.Exit(2)
	}
}

func configCmd(cmd string, args []string) {
	c := LoadConfig()
	switch cmd {
	case "notify":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			fmt.Println(L("Notifications: ", "Bildirimler: ") + map[bool]string{true: "on", false: "off"}[c.Notify])
			fmt.Println(L("Change with: kerna notify on|off", "Değiştirmek için: kerna notify on|off"))
			return
		}
		c.Notify = args[0] == "on"
		if err := SaveConfig(c); err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(1)
		}
		fmt.Println(L("Notifications are "+args[0]+". They need the recording service: kerna service install", "Bildirimler "+args[0]+". Kayıt servisi gerekir: kerna service install"))
	case "ignore":
		if len(args) == 0 {
			if len(c.Ignore) == 0 {
				fmt.Println(L("Nothing is muted. Mute one with: kerna ignore <id>", "Susturulmuş bir şey yok. Susturmak için: kerna ignore <id>"))
			}
			for _, id := range c.Ignore {
				fmt.Printf("%-18s %s\n", id, titleOf(id))
			}
			fmt.Println(L("Known ids: ", "Bilinen id'ler: ") + strings.Join(knownIDs(), ", "))
			return
		}
		if err := validID(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "kerna:", err)
			os.Exit(2)
		}
		if c.Mute(args[0]) {
			SaveConfig(c)
		}
		fmt.Println(L("Muted: ", "Susturuldu: ") + titleOf(args[0]))
	case "unignore":
		if len(args) != 1 {
			usage()
			os.Exit(2)
		}
		if c.Unmute(args[0]) {
			SaveConfig(c)
		}
		fmt.Println(L("Back on: ", "Geri açıldı: ") + args[0])
	}
}

func whyFroze(pal palette) {
	boots := []BootReport{scanBoot(-1), scanBoot(0)}
	var last []Entry
	if b := boots[0]; b.Checked && !b.Clean && !b.Ended.IsZero() {
		es, _ := readHistory(historyPath(), b.Ended.Add(-10*time.Minute))
		for _, e := range es {
			if !e.T.After(b.Ended) {
				last = append(last, e)
			}
		}
	}
	for _, l := range FrozeLines(pal, boots, last) {
		fmt.Println(l)
	}
}
