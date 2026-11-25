#!/bin/sh
# kerna installer: downloads the latest release, verifies its checksum and
# installs it. No root needed: it goes to ~/.local/bin (or /usr/local/bin as root).
# Read it before you run it:
#   curl -fsSL https://raw.githubusercontent.com/vam0laa/kerna/main/install.sh | sh
# Language: English, or Turkish when your locale is Turkish (force with KERNA_LANG=tr|en).
set -eu

BASE="${KERNA_BASE_URL:-https://github.com/vam0laa/kerna/releases/latest/download}"

loc="${KERNA_LANG:-${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}}"
case "$loc" in tr*|TR*) TR=1 ;; *) TR=0 ;; esac
say() { if [ "$TR" = 1 ]; then printf '%s\n' "$2"; else printf '%s\n' "$1"; fi; }
die() { say "kerna: $1" "kerna: $2" >&2; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" "desteklenmeyen işlemci: $(uname -m)" ;;
esac
[ "$(uname -s)" = Linux ] || die "Linux only" "yalnızca Linux"

if command -v curl >/dev/null 2>&1; then get() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then get() { wget -q "$1" -O "$2"; }
else die "curl or wget is required" "curl veya wget gerekli"; fi

if [ "$(id -u)" = 0 ]; then DEST=/usr/local/bin; else DEST="${HOME}/.local/bin"; fi
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

TARBALL="kerna-linux-$ARCH.tar.gz"
say "Downloading $TARBALL" "$TARBALL indiriliyor"
get "$BASE/$TARBALL" "$TMP/$TARBALL" || die "download failed" "indirme başarısız"
get "$BASE/SHA256SUMS" "$TMP/SHA256SUMS" || die "download failed" "indirme başarısız"

say "Verifying checksum" "Sağlama toplamı doğrulanıyor"
( cd "$TMP" && grep " $TARBALL\$" SHA256SUMS | sha256sum -c - >/dev/null 2>&1 ) \
  || die "checksum mismatch, nothing was installed" "sağlama toplamı tutmuyor, hiçbir şey kurulmadı"

mkdir -p "$DEST"
tar -xzf "$TMP/$TARBALL" -C "$TMP" kerna
install -m 755 "$TMP/kerna" "$DEST/kerna"

say "Installed: $DEST/kerna" "Kuruldu: $DEST/kerna"
case ":$PATH:" in *":$DEST:"*) ;; *) say "Add $DEST to your PATH to run it as: kerna" "kerna olarak çalıştırmak için $DEST yolunu PATH'ine ekle" ;; esac
say "Try:  kerna      (live view: kerna watch)" "Dene: kerna      (canlı görünüm: kerna watch)"
