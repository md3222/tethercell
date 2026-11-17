#!/bin/bash
# Manual APK build (no Gradle). Output: build/tethercell.apk
set -euo pipefail
cd "$(dirname "$0")"
SDK=${ANDROID_HOME:-$HOME/work/tools/android-sdk}
BT=$SDK/build-tools/36.0.0
JAR=$SDK/platforms/android-36/android.jar
B=build/apk; rm -rf $B; mkdir -p $B/classes $B/res-compiled
# Signing key = the one the installed app was signed with (orig. build/debug.keystore). Never regenerate it
# silently: a new key makes "adb install -r" fail (UPDATE_INCOMPATIBLE) until the app is uninstalled.
KS=${TETHERCELL_KS:-$(cd .. && pwd)/keys/usbtether.keystore}
[ -f "$KS" ] || { echo "missing signing key $KS (restore it; or TETHERCELL_NEWKEY=1 to make a new one = phone uninstall needed)";
  [ "${TETHERCELL_NEWKEY:-}" = 1 ] || exit 1
  mkdir -p "$(dirname "$KS")"; keytool -genkeypair -keystore "$KS" -storepass android -keypass android \
  -alias dbg -keyalg RSA -keysize 2048 -validity 10000 -dname "CN=mx tethercell" >/dev/null 2>&1; }
$BT/aapt2 compile --dir app/res -o $B/res-compiled
$BT/aapt2 link -o $B/base.apk -I $JAR --manifest app/AndroidManifest.xml \
  --min-sdk-version 29 --target-sdk-version 36 --java $B/rjava $B/res-compiled/*.flat
javac --release 17 -cp $JAR:build/aar/classes.jar -d $B/classes \
  $(find app/src $B/rjava -name '*.java')
$BT/d8 --release --min-api 29 --lib $JAR --output $B $(find $B/classes -name '*.class') build/aar/classes.jar
cp $B/base.apk $B/unsigned.apk
( cd $B && zip -q unsigned.apk classes.dex )
mkdir -p $B/lib/arm64-v8a && cp build/aar/jni/arm64-v8a/*.so $B/lib/arm64-v8a/
( cd $B && zip -q -r -0 unsigned.apk lib )
$BT/zipalign -f -p 4 $B/unsigned.apk $B/aligned.apk
$BT/apksigner sign --ks "$KS" --ks-pass pass:android --key-pass pass:android \
  --out build/tethercell.apk $B/aligned.apk
$BT/apksigner verify build/tethercell.apk && ls -la build/tethercell.apk
