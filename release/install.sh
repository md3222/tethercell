#!/bin/sh
# TetherCell installer — Linux x86-64 + Android phone over USB (AOA).
#
#   sh install.sh                 latest release from GitHub
#   sh install.sh v0.1.0          a specific tag
#   sh install.sh --local DIR     use tethercell + tethercell.apk already on disk (e.g. dist/)
#
# Needs: curl or wget, sudo (graphical password dialog is fine), udev.
# It installs the desktop half; the phone half is the APK it leaves next to it.
set -eu

REPO=${TETHERCELL_REPO:-md3222/tethercell}
MODE=remote
VER=latest
while [ $# -gt 0 ]; do
    case $1 in
        --local) MODE=local; LOCAL=${2:?--local needs a directory}; shift 2 ;;
        -h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) VER=$1; shift ;;
    esac
done

say() { printf 'tethercell: %s\n' "$*"; }
die() { printf 'tethercell: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "the desktop half is Linux-only for now (a Windows port is planned, not released)"
[ "$(uname -m)" = x86_64 ] || die "x86-64 only for now, got $(uname -m)"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/tethercell-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ "$MODE" = local ]; then
    [ -f "$LOCAL/tethercell" ] || die "$LOCAL/tethercell not found"
    [ -f "$LOCAL/tethercell.apk" ] || die "$LOCAL/tethercell.apk not found"
    say "using $LOCAL"
    cp "$LOCAL/tethercell" "$LOCAL/tethercell.apk" "$tmp/"
    [ -f "$LOCAL/SHA256SUMS" ] && cp "$LOCAL/SHA256SUMS" "$tmp/"
else
    if [ "$VER" = latest ]; then
        base="https://github.com/$REPO/releases/latest/download"
    else
        base="https://github.com/$REPO/releases/download/$VER"
    fi
    say "downloading $VER from $REPO"
    fetch() {
        if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
        elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
        else die "need curl or wget"; fi
    }
    ( cd "$tmp" && fetch "$base/SHA256SUMS" SHA256SUMS && fetch "$base/tethercell" tethercell && fetch "$base/tethercell.apk" tethercell.apk )
fi

if [ -f "$tmp/SHA256SUMS" ]; then
    ( cd "$tmp" && sha256sum -c SHA256SUMS ) || die "checksum mismatch: do not run these files"
else
    say "no SHA256SUMS shipped, skipping the checksum"
fi

chmod +x "$tmp/tethercell"
say "installing the desktop half (one password prompt)"
"$tmp/tethercell" setup

apk=${TETHERCELL_APK:-$HOME/tethercell.apk}
"$tmp/tethercell" apk "$apk" >/dev/null && say "phone app written to $apk"

cat <<EOF

Next:
  1. phone app: either   adb install -r "$apk"
                or copy the APK to the phone and open it in Files
                (allow "install unknown apps" when asked)
  2. replug the USB cable once, so the new udev rule applies
  3. run:                tethercell

Status any time:         tethercell status
Stop:                    Ctrl-C in its window
EOF
