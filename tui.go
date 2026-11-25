package main

import (
	"fmt"
	"os"
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
	have, paused, showHistory := false, false, false

	draw := func() {
		cols, rows := termSize()
		var lines []string
		switch {
		case showHistory:
			es, _ := readHistory(historyPath(), time.Now().Add(-24*time.Hour))
			lines = HistoryLines(pal, es, 24*time.Hour, cols-2)
		case !have:
			lines = []string{"", "  " + pal.c(cDim, L("measuring…", "ölçülüyor…"))}
		default:
			note := ""
			if paused {
				note = L("paused", "duraklatıldı")
			}
			lines = Frame(pal, rep, fnds, hist, cols-2, host, time.Now(), note, true)
		}
		foot := L("q quit  ·  p pause  ·  h history", "q çık  ·  p duraklat  ·  h geçmiş")
		if showHistory {
			foot = L("q quit  ·  h back", "q çık  ·  h geri")
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
	for {
		select {
		case rep = <-reports:
			fnds, _ = cfg.Filter(Diagnose(rep))
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
				showHistory = !showHistory
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
