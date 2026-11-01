#!/bin/bash
# Manual APK build (no Gradle). Output: build/usbtether.apk
set -euo pipefail
cd "$(dirname "$0")"
SDK=${ANDROID_HOME:-$HOME/work/tools/android-sdk}
BT=$SDK/build-tools/36.0.0
JAR=$SDK/platforms/android-36/android.jar
B=build/apk; rm -rf $B; mkdir -p $B/classes $B/res-compiled
[ -f build/debug.keystore ] || keytool -genkeypair -keystore build/debug.keystore -storepass android -keypass android \
  -alias dbg -keyalg RSA -keysize 2048 -validity 3650 -dname "CN=mx usbtether debug" >/dev/null 2>&1
$BT/aapt2 compile --dir app/res -o $B/res-compiled
$BT/aapt2 link -o $B/base.apk -I $JAR --manifest app/AndroidManifest.xml \
  --min-sdk-version 29 --target-sdk-version 36 $B/res-compiled/*.flat
javac --release 17 -cp $JAR:build/aar/classes.jar -d $B/classes $(find app/src -name '*.java')
$BT/d8 --release --min-api 29 --lib $JAR --output $B $(find $B/classes -name '*.class') build/aar/classes.jar
cp $B/base.apk $B/unsigned.apk
( cd $B && zip -q unsigned.apk classes.dex )
mkdir -p $B/lib/arm64-v8a && cp build/aar/jni/arm64-v8a/*.so $B/lib/arm64-v8a/
( cd $B && zip -q -r -0 unsigned.apk lib )
$BT/zipalign -f -p 4 $B/unsigned.apk $B/aligned.apk
$BT/apksigner sign --ks build/debug.keystore --ks-pass pass:android --key-pass pass:android \
  --out build/usbtether.apk $B/aligned.apk
$BT/apksigner verify build/usbtether.apk && ls -la build/usbtether.apk
