# Contributing to kerna

Thank you. kerna is small on purpose, and the rules below keep it that way.

## What kerna is

A tool that explains **why** a Linux system is slow, hot or draining its battery. A number nobody can act on does not belong here; a finding does: *what* is wrong, *which program* is behind it, the *evidence*, and *one command* to try.

## Rules

- **Read-only.** kerna reads `/proc` and `/sys`. Two commands run another program (`why-froze` runs `journalctl`, notifications run `notify-send`); do not add a third without a very good reason.
- **No network, no telemetry, no dependencies.** Standard library only.
- **Never claim more than the data shows.** If the kernel does not give watts per program, say "ranked by CPU work", not "uses 5 W". Say what was and was not tested.
- **Every finding is tested against a fake machine.** `kerna_test.go` builds a fake `/proc` and `/sys` in a temp directory (`newFixture`), so a rule can be tested without the hardware. A new rule without a test will not be merged.
- **English and Turkish.** Every user-visible string goes through `L("english", "türkçe")`. Numbers use `fl()` so Turkish gets a decimal comma.
- **Never suggest pausing the desktop.** Names in `protected` (rules.go) must not get a `kill` hint.

## Working on it

```bash
make test        # go vet + unit tests
make build       # ./kerna
./kerna check    # what kerna sees on this machine
```

To try a change against another machine's data, copy its `/proc` and `/sys` somewhere and point `KERNA_ROOT` at it.

## Reporting a problem

Open an issue and paste the output of `kerna check` (it contains no host name, user name or file names).
