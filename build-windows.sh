#!/bin/bash
# Cross-compile the Windows build on Linux. No Windows, no cgo, no mingw.
#
#   ./build-windows.sh            -> dist/tethercell.exe
#   ./build-windows.sh --apk      also rebuild the phone APK first (needs the SDK)
#
# What you get today: a real PE32+ console binary with the CLI surface, the
# embedded phone APK (`tethercell.exe apk`), version/status/help. `up` parses the
# same flags and exits 4 with what is missing: wintun.dll + WinUSB.
set -euo pipefail
cd "$(dirname "$0")"

if [ "${1:-}" = "--apk" ]; then
    ./dist.sh   # builds the APK and the Linux binary; keeps assets/ current
    set -- ""
fi

VC=$(grep -o 'android:versionCode="[0-9]*"' phone-app/app/AndroidManifest.xml | grep -o '[0-9]*')
A=clone/cmd/tethercell/assets
if [ -f phone-app/build/tethercell.apk ]; then
    cp phone-app/build/tethercell.apk "$A/tethercell.apk"
    echo "$VC" > "$A/apk_version.txt"
fi

if TAG=$(git describe --tags --exact-match 2>/dev/null); then
    VER="$TAG-win"
else
    VER="$(date +%Y%m%d)-$(git -C clone rev-parse --short HEAD)-win"
fi

mkdir -p dist
(cd clone && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=$VER" \
    -o ../dist/tethercell.exe ./cmd/tethercell)

ls -lh dist/tethercell.exe
echo "windows build: $VER (embedded phone app versionCode $VC)"
echo "install on the Windows box: copy dist/tethercell.exe, run 'tethercell.exe status'"
