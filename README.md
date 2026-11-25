# kerna

**Why is my Linux system slow, hot or draining the battery?** Tools like btop, glances and fastfetch
show you numbers. `kerna` reads the same kernel data and tells you the *reason*, names the program
behind it, and suggests what to try. In English or Türkçe.

```
$ kerna
kerna  why is my system like this

  CPU 100%   load 13.6/16   RAM 47% free   swap 0 MiB used
  stalled: cpu 71%  disk 0%  memory 0%   92 °C
  BAT1 65% discharging, 57.6 W, ~0.7 h
  GPU nvidia: active

  ✖ Programs are waiting for the CPU
    For 71% of the last 10 seconds at least one task was ready to run but had no free core. This is what makes the system feel slow.
      • sh ×41 1426% of one core
      • qs (pid 1248) 46% of one core
    → pkill -STOP -x sh   # pause them to confirm; pkill -CONT -x sh resumes

  ⚠ The NVIDIA GPU is awake
    The discrete GPU is powered on and its runtime power management is set to "on", so the kernel is not allowed to switch it off, even when no program needs it.
      • /sys/bus/pci/devices/0000:01:00.0/power/control = on
    → echo auto | sudo tee /sys/bus/pci/devices/0000:01:00.0/power/control   # until reboot. ...
```

When nothing is wrong it says so in one line instead of showing you a wall of graphs.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/vam0laa/kerna/main/install.sh | sh
```

No root needed: it installs to `~/.local/bin`, after checking the SHA-256 of the download. Or grab
a tarball from the [releases](https://github.com/vam0laa/kerna/releases/latest), or `make build`.

## Use

```bash
kerna                      # sample for 2 seconds and explain what is wrong
kerna watch                # live view (keys below)
kerna --json               # the same diagnosis as JSON, for scripts and status bars
kerna history              # problems in the last 24 hours (--since 3d for longer)
kerna history --at 03:12   # what was going on at that moment
kerna service install      # record history in the background (a systemd user service)
kerna service uninstall    # stop recording
```

`kerna watch` keys: `q` quit, `p` pause, `j`/`k` (or Tab) select a finding, `c` copy its command to the
clipboard (`wl-copy`, `xclip` or `xsel`), `i` mute it, `t` list the busiest programs (CPU, wake-ups,
RAM), `h` history of the last 24 hours, `d` today's summary.

`kerna watch` is one calm screen on purpose: a verdict on top, six live lines (a short graph and the
number), the busiest programs, and below them only what is wrong. No process tables, no tabs, no mouse.

### History: "what happened at 03:12?"

`kerna service install` starts a small background service that writes one line every 15 seconds to
`~/.local/state/kerna/history.jsonl` (private to your user, deleted after 7 days). Then:

```
$ kerna history
  Last 24 hours, 2 problems:

  Wed 25 Nov
    03:12–03:19 ● Programs were waiting for the CPU  peak stall 71%
                   ffmpeg ×3 270% of one core
    14:02–14:40 ● The battery was draining fast  21 W
```

Nothing is recorded until you install the service, and uninstalling it stops it at once.

### Today, and sleep

```
$ kerna history --today
  Recorded 6.2 hours, 1488 samples.
  Battery
    On battery 4.1 hours, about 98 Wh used, average 24.0 W, highest 41 W at 14:12.
    ··········▁▁▃▅▅▆▅▃▃▂···          average watts per hour, 00 to 23
  Busiest programs (average, in percent of one core)
    qs · Quickshell, your desktop shell     38%
    firefox                                 21%
```

`kerna sleep` shows the kernel's suspend counters (how many suspends failed and which device stopped
one), warns when the machine sleeps in `s2idle` although `deep` is available, and lists long gaps in the
history on battery with the percent lost per hour. A laptop that is really off drains almost nothing, so
a high rate means it was asleep and something kept it busy. Needs the history recorder.

### Why did it freeze?

```
$ kerna why-froze
  ● The previous session: it ended without a normal shutdown  Wed 25 Nov 03:12:40
    Most likely: Memory ran out and a program was killed  03:11:58
      kernel: Out of memory: Killed process 4242 (firefox) total-vm:9000000kB
```

It reads the kernel and system log of the previous and the current boot, checks whether the
previous one ended with a normal shutdown, and names the last memory, graphics, thermal or disk
error shortly before the end. With the history recorder it also shows what kerna saw in the last
minutes. A hard freeze or a power loss leave no trace in any log, and then it says exactly that.
**This is the one command that runs another program: `journalctl`, read-only.**

### Did my change help?

```
$ kerna snapshot        # measure now
$ sudo ...              # change something
$ kerna compare         # measure again and show the difference
  Battery draw  33.8 W      29.1 W      ↓ 4.7 W better
  NVIDIA GPU    active      suspended   suspended ✔
```

Small changes wobble from one sample to the next, so repeat it before you trust a difference.

### In your bar, and as notifications

```jsonc
// ~/.config/waybar/config.jsonc
"custom/kerna": { "exec": "kerna waybar", "return-type": "json", "interval": 10, "tooltip": true }
```

```css
/* ~/.config/waybar/style.css */
#custom-kerna.warn { color: #fbbf24; }  #custom-kerna.bad { color: #f87171; }
```

The class is `ok`, `note`, `warn` or `bad`; the tooltip lists the findings. `kerna notify on` makes
the recording service send a desktop notification (through `notify-send`) when a problem lasts more
than a minute, at most once every 30 minutes per problem. Notifications are off by default.

### Mute what you do not care about

```bash
kerna ignore dgpu-awake    # stop reporting it (kerna ignore lists them, kerna unignore brings it back)
```

Optional thresholds live in `~/.config/kerna/config.json`: `stall_warn` (default 10), `stall_bad`
(30), `hot_c` (88) and `drain_watts` (15).

Language follows `LANG`; force it with `KERNA_LANG=tr` or `KERNA_LANG=en`.

## How it decides

It does not guess from "CPU is high". It uses the kernel's **pressure stall information** (PSI,
`/proc/pressure/*`): the share of the last 10 seconds in which tasks were actually *stalled*
waiting for the CPU, the disk or memory. That is the thing you feel as "slow". A busy CPU that
nobody waits for is not reported.

| Finding | Evidence it needs |
|---|---|
| Programs are waiting for the CPU | CPU stall at least 10%; the busiest programs, same-name processes added together |
| Programs are waiting for the disk | disk stall at least 10%; per-process read and write rate |
| Memory is running out | memory stall, little available RAM, or heavy swap traffic; the biggest programs |
| The CPU is slowing itself down | 88 °C or more, clocks under 70% of maximum, CPU busy |
| A discrete GPU is awake (hybrid laptops) | runtime power state, `power/control`, and for NVIDIA who has `/dev/nvidia*` open |
| A GPU is almost fully busy | 95% or more: `gpu_busy_percent` (amdgpu), or derived from idle residency (Intel i915 and xe) |
| Network connections are losing packets | 3% or more of the TCP segments sent had to be sent again (`/proc/net/snmp`), at least 300 segments |
| The Wi-Fi signal is weak | -75 dBm or worse, when the driver fills `/proc/net/wireless` |
| A performance power profile is active on battery | `platform_profile` or the CPU energy preference is `performance` while discharging |
| The battery is draining fast | on battery, 15 W or more; the suspects ranked by CPU work and wake-ups per second (same-name processes added together), the GPU's own watts (amdgpu) and whether the dGPU is awake |

Programs with names people do not recognise get a plain description (`qs · Quickshell, your desktop shell`).

Each finding has a title, the reason, the evidence and one command to try. `kerna` never runs that
command itself, and it never suggests pausing parts of your desktop session (the compositor,
PipeWire, ...).

## Privacy and safety

- Read-only: it reads `/proc` and `/sys`. No root needed, nothing on your system is changed.
- No network access at all and no telemetry.
- It runs another program in two cases only: `kerna why-froze` runs `journalctl` (read-only), and
  `kerna notify on` lets the recording service run `notify-send`.
- Files it writes, all in your own home directory and only when you ask: the history
  (`service install`), snapshots (`snapshot`) and the config (`ignore`, `notify`).
- Go standard library only, under 2,000 lines (tests not counted). Read it.

## Limits (please read)

- Linux only. Written and tested on Arch with Hyprland on an AMD + NVIDIA laptop; other setups
  should work for the CPU, disk, memory and thermal findings but have not been tried by us.
- Without root it sees the disk and GPU use of **your own** processes only. A finding can then say
  "no process you own is the main source". Run it with `sudo` to see every user.
- Thermal throttling is detected from temperature and clock speed together, not from a kernel
  flag, so it is an informed estimate.
- The discrete-GPU finding works for any vendor, but only the NVIDIA case (the laptop it was
  built on) was seen on real hardware. The AMD and Intel paths are tested against fake sysfs data.
  Intel GPU busy is derived from idle residency, and amdgpu is the only driver that reports GPU watts.
- Without root the kernel gives no watts per program (the power counters need root). The battery
  finding therefore ranks suspects by CPU work and wake-ups, and says so; it never invents watts.
- `why-froze` needs a journal your user may read (group `wheel`, `adm` or `systemd-journal`) and can
  only name causes that reached the log. It was tried on real logs of a clean shutdown; the
  out-of-memory, graphics and thermal cases were tested with sample log lines. `notify-send` and the
  Waybar module were not run on a live desktop bar or notification daemon yet.
- The Wi-Fi signal is only available when the driver fills `/proc/net/wireless`; on some chips (for
  example the MediaTek mt7925 of the laptop kerna was built on) it stays empty, and kerna then relies on
  the packet loss finding alone. There is no per-program network use without root.
- The watt-hours in `history --today` are an estimate: the reported draw times the time between samples.
- History evidence is saved as text in the language of the recording service.

## Quickshell

`contrib/quickshell/KernaStatus.qml` is a small Quickshell window that shows the verdict and the findings,
refreshed every 10 seconds from `kerna waybar`. Try it with `qs -p contrib/quickshell/KernaStatus.qml`, then
copy its `Process` and `Timer` into your own shell. How the window looks depends on your compositor rules.

## Build

```bash
make build        # ./kerna
make test         # go vet + unit tests, which run against a fake /proc and /sys
```

To diagnose a copy of another machine's `/proc` and `/sys`, point `KERNA_ROOT` at it.

## License

MIT
