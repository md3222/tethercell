> **Renamed 2026-11-16:** this plan was written as `usbtether` / `com.matt.usbtether` / `mxtun0`. The shipped project is now **TetherCell**: `tethercell`, `com.matt.tethercell`, `tcell0`. Old names below are kept as the historical log.

# tetrd-clone phone app — plan (2026-11-01)

Decision: Option 2 — build an original Android app for the phone side, replacing
`com.robskie.tether` entirely for this link. Not Tetrd-protocol-compatible by
design (keeps "no licensing/billing bypass" clean — there's no authorize path
to bypass if we never speak Tetrd's RPC).

## Why not Option A (interop with the existing Tetrd app)

The manifest check (`aapt dump xmltree base.apk AndroidManifest.xml`) shows
`LaunchActivity` *does* have a correct `USB_ACCESSORY_ATTACHED` filter and
`accessory_filter` meta-data, so Android should dispatch to it. The repeated
handshake/reconnect cycles seen in `dumpsys usb`'s USB Event Log are more
likely the existing `tetrd-clone` daemon's ladder implementation failing
partway through (or manual kill/restart during testing) than Android refusing
to launch the app. Not fully diagnosed — moot now that we're not using that
protocol.

## Own identity, not Tetrd's

- AOA strings: manufacturer `mxlab`, model `usbtether`, version `1`,
  description/URI ours. **Must not reuse `robskie`/`tether`** — two apps
  claiming the same accessory match means Android shows a chooser or picks
  unpredictably. Own strings mean `com.robskie.tether` stays installed
  untouched as a fallback; no `pm disable-user` needed.
- Package name: `com.matt.usbtether` (placeholder, pick before first build).
  New host TUN name too — not `tetrd0` (avoids colliding with
  `tetrd-shape.service`, which binds to anything named `tetrd0`).
- No Tetrd RPC ladder, no gob fixture replays, no `AuthorizeDeviceV2`. Wire
  format: bare IP over USB bulk, split by IP total-length
  (`internal/wire.IPLength`, already written and tested). A minimal one-byte
  control frame only if a keepalive turns out to be necessary.

## Reused from `~/work/tetrd-re/clone/`

- `internal/usbdev` — AOA handshake (51/52/53) + usbfs bulk I/O, desktop side.
- `internal/tun` — `/dev/net/tun` ioctl + argv-only `ip(8)` config.
- `internal/wire.IPLength` — IPv4/v6 total-length framing helper.
  The RPC/TLV parts of `wire` and all of `proto` are Tetrd-ladder-specific —
  not reused.

## Phone side (the new work)

- **Java/Kotlin shell**: `LaunchActivity` with the `USB_ACCESSORY_ATTACHED`
  filter + our own `accessory_filter` resource, `UsbManager.openAccessory`,
  hand the resulting `ParcelFileDescriptor` to the Go core.
- **Foreground service**: type `connectedDevice` (Tetrd's own
  `android:foregroundServiceType=0x10`, used here as a reference for which
  type constant applies — not copying its code or permission list verbatim).
- **Go core via `gomobile bind`**: gVisor netstack (`gvisor.dev/gvisor@go`
  branch, module now at `go 1.27.3` after `GOTOOLCHAIN=auto`) on a
  `link/channel` endpoint in promiscuous+spoofing mode, `tcp.NewForwarder` /
  `udp.NewForwarder` re-dialing real Android sockets on mobile data — same
  entry/exit split the RE report found in Tetrd itself (`REA-2026-10-31.md`
  §3), just our own implementation.
- **DNS**: must resolve through a real routable IP via the tunnel
  (e.g. 1.1.1.1), not a virtual address like Tetrd's `10.0.3.69` — nothing
  on our side will serve that, and copying it verbatim is exactly what broke
  IPv4 the first time (desktop had an address/route pointing nowhere useful).

## Toolchain (installed 2026-11-01)

```
~/work/tools/android-sdk/               # cmdline-tools, platform-tools, platforms;android-36, build-tools;36.0.0
~/work/tools/android-sdk/ndk/27.3.13750724/
~/go/bin/gomobile, ~/go/bin/gobind       # go install golang.org/x/mobile/cmd/{gomobile,gobind}@latest
~/work/tetrd-re/phone-app/               # new Go module, go.mod pinned to go1.27.3 (gvisor's requirement)
```

No bashrc edit was made (blocked by the harness as unauthorized persistence —
correctly, that's not mine to decide silently). Export per-session:

```bash
export ANDROID_HOME=$HOME/work/tools/android-sdk
export ANDROID_NDK_HOME=$ANDROID_HOME/ndk/27.3.13750724
export PATH=$PATH:$HOME/go/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin
```

## Test safety (per AGENTS.md + today's incident)

- The AOA switch almost certainly drops `usb0` (USB function changes from
  tether to accessory) — confirm `wlan0` is up as the fallback path *before*
  every attach attempt, and say so before testing.
- First live test: **no route install.** Bring the TUN up, add a /32 host
  route to one test IP by hand, verify with `curl --interface <tun>`, before
  ever touching the default route.
- Assert the TUN's IPv4 address after bring-up and treat every `ip(8)` call
  as fatal on error — the original incident was `tetrd0` ending up with only
  an IPv6 address despite the code asking for both; NetworkManager showing
  `tetrd0` as `connected:tetrd` (managed) is suspect. Plan: either
  `nmcli dev set <iface> managed no` right after creating the TUN, or
  confirm NM isn't what strips the v4 address, before the next live test.
- Stopping/removing anything running (services, routes, the real Tetrd
  daemon if ever touched) gets logged with `blocked`.

## Status

- [x] Toolchain installed and verified (SDK, NDK, gomobile, gvisor `go`
      branch fetched into a dedicated module).
- [x] Final names: pkg `com.matt.usbtether`, AOA mxlab/usbtether v1, TUN `mxtun0`, host addr 10.77.0.1/24.
- [x] Android app (no Gradle): `phone-app/build-apk.sh` -> `build/usbtether.apk` (aapt2/javac/d8/apksigner, debug key). Installed on phone 2026-11-01.
- [x] Go core `phone-app/core/core.go` (gVisor netstack + TCP/UDP forwarders) bound to `build/core.aar` (arm64, android 29).
- [x] Wired in TunnelService (openAccessory -> detachFd -> Core.start). Host: `clone/cmd/usbtether-host` -> `clone/bin/usbtether-host`.
- [ ] Build, install via `adb install -r`, first attach test (no route).
- [ ] DNS: pick and wire a real upstream resolver through the tunnel.
- [ ] Route test, then full default-route test, with `wlan0` confirmed live
      as fallback throughout.

## Live test log (2026-11-01)

- Ran `usbtether-host -hostroute 1.1.1.1/32` 4x. Phone received GETPROTOCOL/SENDSTRING/START uevents
  (dumpsys usb) but **never left RNDIS** (stays 04e8:6864; no USB_STATE cycle). Tried: our strings (serial last),
  serial-first, version "1", and a diagnostic `-probe-profile tetrd` (stock robskie/tether identity) -> same.
  So it is NOT our strings/app. Prime suspect: stale accessory/gadget state on the phone from earlier sessions
  (dumpsys still shows old robskie `current_accessory`). `adb shell svc usb resetUsbGadget` had no visible effect.
- Next: physically replug the phone (or toggle USB tethering off/on) and rerun; if still no switch, usbmon-capture
  our control transfers against the known-good ones in REA §1 (wLength / timing / GET_PROTOCOL wLength 64 vs 2).

## RESULT 2026-11-01 01:20 — first end-to-end success

- Root cause of the earlier "phone ignores START": `usbdev.SendString` put the string index in wValue instead of wIndex
  (phone log showed `mAccessoryStrings[0]=<last string>`, rest null, `enableAccessory=false`). Fixed in `clone/internal/usbdev/usbdev.go`.
- Verified through `mxtun0` with only a /32 host route to 1.1.1.1 (default route untouched, wlan0 stayed primary):
  `curl --interface mxtun0 https://1.1.1.1` -> 301; UDP DNS `dig -b 10.77.0.1 @1.1.1.1 example.com` -> answers;
  tun RX/TX counters climbing. Phone app log: `mxusbtether: tunnel started`.
- Known gaps: ICMP echo not forwarded (ping fails); app needs the phone screen ON/unlocked at attach time or Android
  will not launch the activity (accessory mode then times out ~15s and the phone reverts to MTP); app start takes ~1-4s
  after the AOA switch (host now drops timed-out writes instead of exiting); host needs a cable replug between runs
  (phone stays in accessory mode with our identity); no IPv6; no default-route / DNS test yet.
- Next: (1) ICMP echo forwarding via real ping sockets, (2) default-route test with `-route` while wlan0 is up
  (metric 50), (3) throughput test (iperf/curl large file), (4) reconnect without a replug, (5) drop root after TUN
  creation / use CAP_NET_ADMIN.

## RESULT 2026-11-01 01:50 — full default-route test passed

- ICMP echo forwarding added (unprivileged ICMP datagram socket on Android): ping via tunnel 0% loss, ~50 ms idle RTT
  (spikes to 300-800 ms under load = bufferbloat, no shaping yet).
- Default route (`usbtether-host -reuse -route`, metric 50): github/nytimes/stackoverflow/wikipedia/reddit all load.
  3x 25 MB downloads complete: 5.8 s, 12.9 s, 7.2 s (~15-35 Mbit/s). Plain USB tether is still faster (~300 Mbit/s, per notes).
- Bugs found+fixed on the way: (1) usbdev.SendString index in wValue not wIndex; (2) Android launches the app mid-switch
  while the gadget flaps -> service now retries opening the accessory (`TunnelService.loop`); (3) core treated a transient
  accessory-read EIO as fatal -> TCP resets; now retries (up to 200 consecutive errors); (4) phone screen timeout
  (~15-30 s) drops accessory mode -> need screen on; used `svc power stayon usb` during tests and RESTORED it to false after.
- `-reuse` lets the host re-attach to an existing accessory session (no replug); launch the service with
  `adb shell am start -n com.matt.usbtether/.MainActivity`.
- Open: IPv6 not implemented; host still root; throughput/latency tuning (bigger URB queue / async bulk, SO_SNDBUF, cake);
  no auto-start; screen-off behavior (needs foreground wake or stay-awake); nothing committed to git yet.

## FINAL 2026-11-01 02:08 — all open items closed (see phone-app/README.md)

- Speed: host batches TUN->USB writes (+0x00 pad off 512-multiples); phone-side batching measured SLOWER/erratic -> `maxBatch=0`.
  Result: 3x25 MB down = 94/58/78 Mbit/s, up ~16 Mbit/s, RTT under saturated download ~37 ms.
- Privilege: host drops to SUDO_UID after TUN/usbfs/routes exist (+ no_new_privs, verified cannot regain root). Not the slowdown (A/B tested).
- Screen-off: tunnel survived 40 s+ with the screen Dozing and traffic flowing -> no wake lock needed; earlier drops were the fatal-EIO / early-launch bugs.
- IPv6: `-v6` gives mxtun0 fd77:7777::1/64 + v6 default with -route; TCP over v6 works (cloudflare, google). ICMPv6 echo is answered
  locally by the phone netstack (not forwarded) — don't trust v6 ping as a path test.
- Git: local repos (no remote): ~/work/tetrd-re/phone-app and ~/work/tetrd-re/clone. Build outputs/keystore gitignored.
- Phone setting `stay_on_while_plugged_in` restored to 0. TEMP sudoers zz-claude-temp removed at end of session.

## 2026-11-01 03:01 stall + post-reboot crash loop → fixes (host 5dbf819, app 8d22d44)

- 02:56–03:01: new build ran ~120 Mbit/s avg (peak 140) on a Win11 ISO, 0 write timeouts. At 03:01:21 it died for good.
  Cellular stayed CONNECTED+VALIDATED. The phone core kept parsing host packets (ping/udp/tcp counters up, tcpErr=0)
  and writing to USB, but nothing reached mxtun0, even across 3 fresh host processes. The old any-byte watchdog never tripped.
- After the PC reboot: `SecurityException: Starting FGS with type connectedDevice` on every launch (Android 14+
  precondition), then Android crash backoff (30–90 min). Also `claim interface 0: busy` at 03:08 (stock Tetrd daemon).
  Matt fell back to stock Tetrd.
- Fixes:
  - Per-transfer framing with IP validation on both ends (`clone/internal/frame`, `phone-app/core/frame.go`).
  - Valid-packet watchdog (`-dead-after 10s`) with an active echo probe after 3 s (so idle pure ACKs do not trip it),
    `-wedge-after 6s` on USB write timeouts, exit 3 (died after working) vs 4 (never delivered); clone 2nd commit.
  - 10 s host stats line; phone `core stats` gained bad=/outB=.
  - CLEAR_HALT on attach; serialized phone writes (ping replies included).
  - App: CHANGE_NETWORK_STATE + guarded startForeground (no more crash loop); opens only the mxlab/usbtether accessory.
  - Wrapper: exit 3 → reconnect (unlimited); 4 → force-stop + relaunch the phone app, give up after 3; other → retry 5 s × 12.
  - Still unverified live: idle use without speedwatch (probe), force-stop + reopen of the accessory, CLEAR_HALT on this phone.
- Root cause of the 03:01 stall is still unproven (framing desync suspected). The next stall's host `stats 10s` and phone
  `core stats` lines will show which hop stopped.
- Verified: app launch with no mxlab accessory → FGS allowed, clean stopSelf, no crash; frame unit tests pass;
  wrapper loop tested with a stub. NOT yet live-tested: needs Tetrd quit + one replug.

## ROOT CAUSE FOUND 2026-11-01 03:55 (clone af973df)
`usbdev.BulkRead` returned the *requested* length (`t.Len`, always 16384) instead of the ioctl result (bytes actually
received). Every read gave the parser one real packet plus ~16 KB of stale buffer. The old stream parser misread that
garbage as IP headers with bogus lengths: lost/duplicated packets, 5 s DNS stalls, 14% slow-ping loss, and most likely
the 03:01 wedge. The per-transfer validator (5dbf819) masked it: 03:42–03:51 ran fine but logged 89,750 bad frames,
5.92 GB "read" vs 1.57 GB actually sent by the phone. Fixed for read and write. Expect host `bad=` ≈ 0 after a restart.
Matt's Ctrl-C/rerun tests 03:36–03:41: each rerun re-attached within seconds, no replug. The exit-3 auto-reconnect
has not fired live yet (no stalls).
- VERIFIED 03:55 on af973df (restart 03:53:52): host bad=0, usbIn byte counts realistic; speed.cloudflare.com over the tunnel (IPv6): 133 down / 30.2 up Mbit/s, 56 ms latency (81 loaded down, 145 loaded up), 0% loss of 1000. Best result yet; upload latency spread = bufferbloat (optional: cake on mxtun0 egress).

## v3 SPEED WORK 2026-11-11 (built, NOT live-tested)

- Link MTU 1400 -> 16000 on both ends (frame MaxPacket 16384). Phone terminates TCP, so this is USB-only.
- Phone: gVisor TCP SACK, cubic, moderated receive buffer, 1-8 MB send/recv buffers (`tuneTCP`). APK versionCode 3.
- Host: async bulk-IN, 8 usbfs URBs in one mmap, poll(POLLOUT)+REAPURBNDELAY (500 ms stop wake-up kept).
- A/B flags: `-urbs 0` = old sync read; `-mtu 1400` = old packet sizes (phone follows host MSS).
- Commits: clone e252996, 50ded11, b6f58a2; phone-app 96f4c8a. Baseline to beat: 133 down / 30 up.
- New, never-exercised path: phone->host ~16 KB transfers. If host bad>0, try `-mtu 1400` first.
- 2026-11-11 04:55: LIVE: -urbs 16 -mtu 13000 -> 243/31.8 Mbit/s (was 133/30), loaded ping 200/360 ms.
- 2026-11-11: v4 + defaults (urbs 8, mtu 16000) = same ~240 down, lower ping. Final; no default change needed.
