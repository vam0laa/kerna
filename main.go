// cause tells you why your Linux system is slow, hot or draining its battery,
// not just what the numbers are. It only reads /proc and /sys, needs no root,
// changes nothing and sends nothing anywhere.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, L(`cause: why is my system slow, hot or draining the battery?

  cause              sample for a few seconds and explain what is wrong
  cause watch        live view, refreshed every few seconds (Ctrl-C to quit)
  cause --json       the same diagnosis as JSON, for scripts and status bars
  cause version

Read-only: it reads /proc and /sys, needs no root and never runs the commands it suggests.
`, `cause: sistem neden yavaş, sıcak ya da pili tüketiyor?

  cause              birkaç saniye örnekle ve neyin yanlış olduğunu açıkla
  cause watch        canlı görünüm, birkaç saniyede bir yenilenir (çıkış: Ctrl-C)
  cause --json       aynı teşhisi betikler ve durum çubukları için JSON olarak ver
  cause version

Salt okunur: /proc ve /sys dosyalarını okur, root gerektirmez ve önerdiği komutları asla çalıştırmaz.
`))
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("cause", flag.ExitOnError)
	fs.Usage = usage
	asJSON := fs.Bool("json", false, "print JSON")
	secs := fs.Float64("n", 2, "sampling window in seconds")
	fs.Parse(args)
	src := Source{Root: "/"}
	if r := os.Getenv("CAUSE_ROOT"); r != "" {
		src.Root = r // used by tests and for diagnosing a copy of /proc and /sys
	}
	win := time.Duration(*secs * float64(time.Second))
	if win < 500*time.Millisecond {
		win = 500 * time.Millisecond
	}

	switch cmd {
	case "version":
		fmt.Println("cause", version)
	case "watch":
		watch(src, win)
	case "", "diagnose":
		rep := src.Sample(win)
		fnds := Diagnose(rep)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(struct {
				Findings []Finding `json:"findings"`
			}{append([]Finding{}, fnds...)})
			return
		}
		Render(os.Stdout, palette{useColor(os.Stdout)}, rep, fnds)
	case "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "cause: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func watch(src Source, win time.Duration) {
	pal := palette{useColor(os.Stdout)}
	fmt.Print("\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	restore := func() { fmt.Print("\x1b[?25h\x1b[?1049l") }
	defer restore()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; restore(); os.Exit(0) }()
	for {
		rep := src.Sample(win)
		fmt.Print("\x1b[H\x1b[2J")
		Render(os.Stdout, pal, rep, Diagnose(rep))
		fmt.Println(pal.c("2", L("  refreshing every few seconds, Ctrl-C to quit", "  birkaç saniyede bir yenilenir, çıkmak için Ctrl-C")))
	}
}
