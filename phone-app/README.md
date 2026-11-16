# usbtether (phone app + core)

## The app (2026-11-03): one desktop binary, phone app inside
```bash
~/work/tetrd-re/dist.sh                 # build APK + dist/usbtether (APK embedded), SHA256SUMS
~/work/tetrd-re/dist/usbtether setup    # once per desktop: one popup -> /usr/local/bin/usbtether
                                        # (cap_net_admin, no root process) + udev uaccess rule; NM profile + menu entry
usbtether                               # = up -route -v6: installs/updates the phone app over adb, connects, reconnects
usbtether status | phone | apk [file] | selftest | run -h
```
- Phone wiped: enable USB debugging, plug in, accept the RSA prompt, run `usbtether` (installs the app itself).
  No USB debugging: `usbtether apk`, copy `~/usbtether.apk` to the phone, install it from Files. Android opens the app
  on attach (accessory filter); tap OK if it asks.
- First attach after an install: Android asks "Open USB Tether for this accessory?" -> tick *Always*, OK.
  Without that grant the service started over adb cannot open the accessory.
- If adb or the APK install is refused after a wipe, check Samsung Auto Blocker (Settings > Security and privacy);
  unverified whether a wipe re-enables it.
- Desktop wiped: copy `dist/usbtether` (it is static and self-contained) and run `usbtether setup`.
- Signing key: `~/work/tetrd-re/keys/usbtether.keystore` (= old build/debug.keystore). Lose it and updates need
  `adb uninstall com.matt.usbtether` first. NOTE: cloud-backup's filter excludes `*.keystore`.
- Phone screen: tap the launcher icon for status/counters/Stop; started by the PC it stays invisible.

Own USB tether, original code. Desktop = `~/work/tetrd-re/clone/cmd/usbtether-host`, phone = this app
(`com.matt.usbtether`). Wire format: bare IP datagrams over the AOA bulk endpoints (own AOA identity
mxlab/usbtether v1; NOT Tetrd-compatible). Phone = exit node: gVisor netstack + TCP/UDP forwarders +
ICMP echo (unprivileged ICMP sockets), re-dialing from real Android sockets.

## Build
```bash
export ANDROID_HOME=$HOME/work/tools/android-sdk ANDROID_NDK_HOME=$ANDROID_HOME/ndk/27.3.13750724
export JAVA_HOME=/usr/lib/jvm/java-21-openjdk-amd64 PATH=$PATH:$HOME/go/bin
gomobile bind -target=android/arm64 -androidapi 29 -javapkg com.matt.usbtether -o build/core.aar ./core
rm -rf build/aar && unzip -o -q build/core.aar -d build/aar && ./build-apk.sh && adb install -r build/usbtether.apk
```
Host: `cd ~/work/tetrd-re/clone && go build -o bin/usbtether-host ./cmd/usbtether-host`

## Everyday use (wrapper, auto-reconnect)
```bash
usbtether-host -route -v6     # ~/.local/bin wrapper: sudo-gui, starts the phone app, logs to ~/work/tetrd-re/logs/host.log
```
- Watchdog: after 3 s with no reply the host pings 1.1.1.1 through the phone every 2 s. It declares the tunnel dead
  after 10 s with no valid packet back, or 6 s of stuck USB writes.
- Exit 3 (died after working) → reconnect, unlimited. Exit 4 (never delivered a packet) → retry with a force-stopped,
  relaunched phone app, give up after 3 ("replug"). Other failures (unplugged) → retry every 5 s for 1 min. Routes are
  gone while it reconnects, so Wi-Fi carries traffic. Ctrl-C stops it. `-clear-halt=false` disables the attach-time
  endpoint reset if a first attach goes straight to write timeouts.
- Stock Tetrd and this cannot share the phone. Quit Tetrd (tray → Quit) first; the wrapper refuses to start while
  `/opt/Tetrd/bin/tetrd` runs. After using Tetrd, **replug the cable once**: the phone otherwise stays in accessory mode
  with Tetrd's identity, and `-reuse` would attach to the wrong app.
- Diagnosing a stall: host `stats 10s: usbIn= toTun= bad= toUSB= wTimeout=` vs phone `adb logcat | grep "core stats"`.

## Run (phone unlocked/awake for the first attach)
```bash
sudo ~/work/tetrd-re/clone/bin/usbtether-host -hostroute 1.1.1.1/32   # safe: one route
sudo ~/work/tetrd-re/clone/bin/usbtether-host -route [-v6]            # default route (metric 50), wlan0 stays fallback
# phone already in accessory mode (no replug):  add -reuse, then: adb shell am start -n com.matt.usbtether/.MainActivity
```
Host drops root after TUN/USB/routes exist (`-drop=false` to disable). Routes vanish when the host exits.

## Measured 2026-11-01 (phone on LTE)
download 58-94 Mbit/s, upload ~16, RTT under saturated download ~37 ms, survives screen-off, IPv4+IPv6 TCP, UDP DNS,
ICMPv4 echo. Writes on the phone are single-packet (`maxBatch=0`): coalescing was slower/erratic. Host batches TUN->USB.

## Known gaps
ICMPv6 echo answered locally (not forwarded); no auto-start/reconnect without the host re-running; not a VPN
(phone's own traffic unaffected); debug-key-signed APK; a transfer is padded with 0x00 when a multiple of 512 bytes.
