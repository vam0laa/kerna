# cause

**Why is my Linux system slow, hot or draining the battery?** Tools like btop, glances and fastfetch
show you numbers. `cause` reads the same kernel data and tells you the *reason*, names the program
behind it, and suggests what to try. In English or Türkçe.

```
$ cause
cause  why is my system like this

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

## Use

```bash
cause              # sample for 2 seconds and explain what is wrong
cause watch        # live view, refreshed continuously (Ctrl-C to quit)
cause --json       # the same diagnosis as JSON, for scripts and status bars
cause -n 5         # sample for 5 seconds instead of 2
```

Language follows `LANG`; force it with `CAUSE_LANG=tr` or `CAUSE_LANG=en`.

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
| The NVIDIA GPU is awake (hybrid laptops) | runtime power state, `power/control`, who has `/dev/nvidia*` open |
| The battery is draining fast | on battery, 15 W or more; with the top consumers and whether the dGPU is awake |

Each finding has a title, the reason, the evidence and one command to try. `cause` never runs that
command itself, and it never suggests pausing parts of your desktop session (the compositor,
PipeWire, ...).

## Privacy and safety

- Read-only: it reads `/proc` and `/sys`. No root needed, nothing is changed.
- No network access at all, no telemetry, no files written.
- Go standard library only, about 1,200 lines. Read it.

## Limits (please read)

- Linux only. Written and tested on Arch with Hyprland on an AMD + NVIDIA laptop; other setups
  should work for the CPU, disk, memory and thermal findings but have not been tried by us.
- Without root it sees the disk and GPU use of **your own** processes only. A finding can then say
  "no process you own is the main source". Run it with `sudo` to see every user.
- Thermal throttling is detected from temperature and clock speed together, not from a kernel
  flag, so it is an informed estimate.
- GPU findings cover NVIDIA hybrid laptops. AMD and Intel GPUs are shown but not diagnosed.
- It diagnoses the last few seconds, not the past. History ("what happened at 03:12?") is not
  built yet.

## Build

```bash
make build        # ./cause
make test         # go vet + unit tests, which run against a fake /proc and /sys
```

To diagnose a copy of another machine's `/proc` and `/sys`, point `CAUSE_ROOT` at it.

## License

MIT
