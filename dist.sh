#!/bin/bash
# Build both halves into one self-contained desktop binary:
#   phone-app (gomobile core + APK, signed with keys/usbtether.keystore) -> embedded into
#   clone/cmd/usbtether -> dist/usbtether (static, CGO_ENABLED=0) + dist/usbtether.apk + SHA256SUMS
# Then: dist/usbtether setup   (once per desktop; one password popup)
set -euo pipefail
cd "$(dirname "$0")"
R=$PWD
export ANDROID_HOME=${ANDROID_HOME:-$HOME/work/tools/android-sdk}
export ANDROID_NDK_HOME=${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/27.3.13750724}
export JAVA_HOME=${JAVA_HOME:-/usr/lib/jvm/java-21-openjdk-amd64}
export PATH=$PATH:$HOME/go/bin

if [ "${1:-}" != --skip-apk ]; then
  (cd phone-app
   gomobile bind -target=android/arm64 -androidapi 29 -javapkg com.matt.usbtether -o build/core.aar ./core
   rm -rf build/aar && unzip -o -q build/core.aar -d build/aar
   ./build-apk.sh)
fi
VC=$(grep -o 'android:versionCode="[0-9]*"' phone-app/app/AndroidManifest.xml | grep -o '[0-9]*')
A=clone/cmd/usbtether/assets
cp phone-app/build/usbtether.apk $A/usbtether.apk
echo "$VC" > $A/apk_version.txt
VER="$(date +%Y%m%d)-$(git -C clone rev-parse --short HEAD)$(git -C clone diff --quiet HEAD -- . 2>/dev/null || echo -dirty)"
mkdir -p dist
(cd clone && go test ./internal/... >/dev/null && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VER" -o "$R/dist/usbtether" ./cmd/usbtether)
cp phone-app/build/usbtether.apk dist/usbtether.apk
(cd dist && sha256sum usbtether usbtether.apk > SHA256SUMS)
ls -la dist; dist/usbtether version
