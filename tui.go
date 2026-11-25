package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// The live view: one calm screen, three keys. Raw terminal mode and the
// window size come straight from ioctl, so no dependency is needed.

func isatty(fd uintptr) bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return e == 0
}

func makeRaw(fd uintptr) (func(), error) {
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&old))); e != 0 {
		return nil, e
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON // keep ISIG so Ctrl-C still works
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); e != 0 {
		return nil, e
	}
	return func() { syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&old))) }, nil
}

func termSize() (cols, rows int) {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, 1, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 || ws.Col == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

func watch(src Source, win time.Duration) {
	if !isatty(0) || !isatty(1) {
		fmt.Fprintln(os.Stderr, L("kerna watch needs a terminal. For scripts use: kerna --json", "kerna watch bir terminal ister. Betikler için: kerna --json"))
		os.Exit(2)
	}
	restoreTTY, err := makeRaw(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kerna:", err)
		os.Exit(1)
	}
	fmt.Print("\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	cleanup := func() { fmt.Print("\x1b[?25h\x1b[?1049l"); restoreTTY() }
	defer cleanup()

	reports := make(chan Report, 1)
	go func() {
		for {
			reports <- src.Sample(win)
		}
	}()
	keys := make(chan byte, 8)
	go func() {
		b := make([]byte, 1)
		for {
			if n, err := os.Stdin.Read(b); n == 1 && err == nil {
				keys <- b[0]
			} else if err != nil {
				return
			}
		}
	}()
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)

	pal := palette{useColor(os.Stdout)}
	cfg := LoadConfig()
	host, _ := os.Hostname()
	hist := &Hist{}
	var rep Report
	var fnds []Finding
	have, paused := false, false
	view := "main" // main, history or today
	sel, showTop := 0, false
	toast, toastUntil := "", time.Time{}
	say := func(s string) { toast, toastUntil = s, time.Now().Add(3*time.Second) }

	draw := func() {
		cols, rows := termSize()
		var lines []string
		switch {
		case view == "history":
			es, _ := readHistory(historyPath(), time.Now().Add(-24*time.Hour))
			lines = HistoryLines(pal, es, 24*time.Hour, cols-2)
		case view == "today":
			es, _ := readHistory(historyPath(), time.Now().Add(-24*time.Hour))
			lines = TodayLines(pal, es, time.Now())
		case !have:
			lines = []string{"", "  " + pal.c(cDim, L("measuring…", "ölçülüyor…"))}
		default:
			note := ""
			if paused {
				note = L("paused", "duraklatıldı")
			}
			lines = FrameOpts(pal, rep, fnds, hist, cols-2, host, time.Now(), note, true, Opts{Sel: sel, Top: showTop})
		}
		foot := L("q quit  ·  p pause  ·  t programs  ·  j/k select  ·  c copy  ·  i mute  ·  h history  ·  d today", "q çık  ·  p duraklat  ·  t programlar  ·  j/k seç  ·  c kopyala  ·  i sustur  ·  h geçmiş  ·  d bugün")
		if view != "main" {
			foot = L("q quit  ·  esc/h/d back", "q çık  ·  h/d geri")
		}
		if toast != "" && time.Now().Before(toastUntil) {
			foot = toast
		}
		if len(lines) > rows-2 {
			lines = append(lines[:rows-3], pal.c(cDim, "  …"))
		}
		var b strings.Builder
		b.WriteString("\x1b[H")
		for _, l := range lines {
			b.WriteString("  " + l + "\x1b[K\r\n")
		}
		b.WriteString("\x1b[J")
		fmt.Fprintf(&b, "\x1b[%d;3H%s", rows, pal.c(cDim, foot))
		os.Stdout.WriteString(b.String())
	}
	draw()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if toast != "" && !time.Now().Before(toastUntil) {
				toast = ""
				draw()
			}
		case rep = <-reports:
			fnds, _ = cfg.Filter(Diagnose(rep))
			if sel >= len(fnds) {
				sel = 0
			}
			if !paused {
				hist.Add(rep)
			}
			have = true
			if !paused {
				draw()
			}
		case k := <-keys:
			switch k {
			case 'q', 'Q', 3:
				return
			case 'p', 'P':
				paused = !paused
				draw()
			case 'h', 'H':
				if view == "history" {
					view = "main"
				} else {
					view = "history"
				}
				draw()
			case 'd', 'D', 27:
				if view == "today" || k == 27 {
					view = "main"
				} else {
					view = "today"
				}
				draw()
			case 't', 'T':
				showTop = !showTop
				draw()
			case 'j', 'J', 9:
				if len(fnds) > 0 {
					sel = (sel + 1) % len(fnds)
				}
				draw()
			case 'k', 'K':
				if len(fnds) > 0 {
					sel = (sel + len(fnds) - 1) % len(fnds)
				}
				draw()
			case 'c', 'C':
				if sel < len(fnds) {
					if cmd := commandOf(fnds[sel]); cmd == "" {
						say(L("this finding has no command to copy", "bu bulgunun kopyalanacak komutu yok"))
					} else if err := copyToClipboard(cmd); err != nil {
						say(L("no clipboard tool found: install wl-clipboard", "pano aracı bulunamadı: wl-clipboard kur"))
					} else {
						say(L("copied the command", "komut kopyalandı"))
					}
				}
				draw()
			case 'i', 'I':
				if sel < len(fnds) {
					id := fnds[sel].ID
					if cfg.Mute(id) && SaveConfig(cfg) == nil {
						say(L("muted "+id+" (kerna unignore "+id+" brings it back)", id+" susturuldu (geri getirmek için: kerna unignore "+id+")"))
						fnds, _ = cfg.Filter(fnds)
						if sel >= len(fnds) {
							sel = 0
						}
					}
				}
				draw()
			}
		case s := <-sig:
			if s == syscall.SIGWINCH {
				draw()
			} else {
				return
			}
		}
	}
}

// commandOf is the command part of a finding's hint, the text before the comment.
func commandOf(f Finding) string {
	cmd, _, _ := strings.Cut(f.Hint, "   # ")
	return strings.TrimSpace(cmd)
}

// copyToClipboard uses whichever clipboard tool is installed.
func copyToClipboard(s string) error {
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-b", "-i"}} {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no clipboard tool")
}
