# Security

kerna only reads files under `/proc` and `/sys`, writes nothing outside your own home directory, makes no network connections and needs no root. It runs another program in two cases only: `kerna why-froze` runs `journalctl`, and `kerna notify on` lets the recording service run `notify-send`.

If you find a way to make kerna do something else, please open a private security advisory on this repository (Security tab, "Report a vulnerability") instead of a public issue.
