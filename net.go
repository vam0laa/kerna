package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// NetCounters are the kernel's running totals; two samples give rates.
type NetCounters struct {
	Rx, Tx, Pkts, Bad  uint64 // bytes in and out, packets, errored or dropped packets (real interfaces only)
	TCPOut, TCPRetrans uint64
}

func realIface(name string) bool {
	if name == "lo" {
		return false
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "vnet", "tun", "tap"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// parseNetDev reads /proc/net/dev.
func parseNetDev(text string) NetCounters {
	var c NetCounters
	for _, line := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || !realIface(strings.TrimSpace(name)) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		n := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		c.Rx += n(0)
		c.Tx += n(8)
		c.Pkts += n(1) + n(9)
		c.Bad += n(2) + n(3) + n(10) + n(11) // errors and drops, both directions
	}
	return c
}

// parseSNMPTcp finds OutSegs and RetransSegs in /proc/net/snmp.
func parseSNMPTcp(text string) (out, retrans uint64) {
	var names []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "Tcp:") {
			continue
		}
		f := strings.Fields(line)[1:]
		if names == nil {
			names = f
			continue
		}
		for i, k := range names {
			if i >= len(f) {
				break
			}
			v, _ := strconv.ParseUint(f[i], 10, 64)
			switch k {
			case "OutSegs":
				out = v
			case "RetransSegs":
				retrans = v
			}
		}
		break
	}
	return
}

func (s Source) readNet() NetCounters {
	var c NetCounters
	if b, err := os.ReadFile(s.p("proc/net/dev")); err == nil {
		c = parseNetDev(string(b))
	}
	if b, err := os.ReadFile(s.p("proc/net/snmp")); err == nil {
		c.TCPOut, c.TCPRetrans = parseSNMPTcp(string(b))
	}
	return c
}

// readWifi returns the first interface in /proc/net/wireless with its level in dBm.
// Some drivers (for example mt7925) leave that file empty; then there is no signal to report.
func (s Source) readWifi() (string, int) {
	f, err := os.Open(s.p("proc/net/wireless"))
	if err != nil {
		return "", 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest) // status link level noise ...
		if len(fields) < 3 {
			continue
		}
		lv, err := strconv.ParseFloat(strings.TrimSuffix(fields[2], "."), 64)
		if err != nil || lv == 0 {
			continue
		}
		if lv > 0 { // some drivers report a positive 0..255 scale: unusable as dBm
			continue
		}
		return strings.TrimSpace(name), int(lv)
	}
	return "", 0
}
