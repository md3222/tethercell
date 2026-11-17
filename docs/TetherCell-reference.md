# TetherCell — command & settings reference

**GUI** `tethercell` (TetherCell) · **host** `usbtether` 20261116-600170f-dirty · **phone app** `com.matt.usbtether` versionCode 6 · 16 Nov 2026

---

## 1. What runs where

| Piece | Path | Role |
|---|---|---|
| Desktop host + supervisor | `/usr/local/bin/usbtether` | AOA handshake, TUN `mxtun0`, routes, reconnect loop |
| TetherCell GUI | `~/work/usbtether/gui/tethercell.py` (symlink `~/.local/bin/tethercell`) | All host flags as controls, live link state, log, latency |
| Phone half | `com.matt.usbtether` (APK embedded in the binary) | gVisor netstack, re-dials every flow from its own sockets |
| Host log | `~/.local/state/usbtether/host.log` | Every session, appended |
| GUI settings | `~/.config/tethercell/config.json` | Widget values + preset, saved on change |
| Builds | `~/work/usbtether/dist/usbtether`, `dist/usbtether.apk` | What `dist.sh` produces |

The desktop half is the only piece that needs a capability (`cap_net_admin`); the GUI itself is unprivileged and simply launches `usbtether up`.

---

## 2. `usbtether` commands

| Command | What it does |
|---|---|
| `usbtether` | Connect and supervise. Same as `up -route -v6`: installs/updates the phone app over adb if present, builds the tunnel, reconnects on failure, Ctrl-C stops. |
| `usbtether up [flags]` | Same supervisor, with your own host flags. `-route` and `-v6` are always prepended; later flags win, so `up -mtu 1400` keeps both, `up -v6=false` turns IPv6 off. |
| `usbtether run [flags]` | The privileged host for exactly one session (what the supervisor spawns). Exits 3/4 to tell the supervisor what broke. |
| `usbtether setup` | One-time desktop install: binary, `cap_net_admin`, udev rule, NetworkManager profile, menu entries. One password popup. |
| `usbtether phone` | Install/update the phone app over adb (force). |
| `usbtether apk [file]` | Write the embedded APK out; default `~/usbtether.apk`, for phones without USB debugging. |
| `usbtether status` | Binary + build id, install state, udev rule, USB mode, phone-app version, `mxtun0` addresses, default route. |
| `usbtether selftest` | Scratch TUN + frame codec test; no phone, no routes. |
| `usbtether version` | Build id, embedded phone-app versionCode, APK size. |
| `usbtether help` | Usage summary. |

Anything else prints `unknown command … (try: usbtether help)` and exits 1.

### 2.1 Host flags (`usbtether run -h`, also valid on `up`)

| Flag | Default | Meaning |
|---|---|---|
| `-iface` | `mxtun0` | TUN interface name |
| `-mtu` | `16000` (576–16000) | TUN MTU. 16000 = max speed on a USB-only link; 1400 = pre-v3 packet sizes. The phone follows the host's MSS. |
| `-urbs` | `8` (0–64) | Async bulk-IN reads kept queued in the kernel. More = deeper pipeline; `0` = old one-at-a-time sync `BulkRead`. This is the "async number". |
| `-wait` | `20s` | How long to wait for the accessory to enumerate |
| `-dead-after` | `10s` | Dead tunnel if data was sent and no valid packet came back for this long |
| `-wedge-after` | `6s` | Dead tunnel if every USB write has timed out for this long |
| `-probe-after` | `3s` | **New.** After this much silence, ask the phone with an ICMP echo |
| `-probe-every` | `2s` | **New.** Minimum gap between those probes; `0` = never probe |
| `-route` | `false` (`true` via `up`) | Install a default route via the TUN, metric 50 (wins over wlan0) |
| `-v6` | `false` (`true` via `up`) | ULA address on the TUN + v6 default route |
| `-hostroute` | empty | Install exactly one route, e.g. `1.1.1.1/32`, via the TUN |
| `-serial` | empty | USB serial of the phone in normal mode |
| `-reuse` | `false` | Attach to a phone already in accessory mode instead of requiring a replug |
| `-drop` | `true` | Drop root to `SUDO_UID` once the TUN/USB fds are open and routes installed |
| `-clear-halt` | `true` | `CLEAR_HALT` both bulk endpoints on attach (resets data toggles) |

The two probe flags are in the 2026-11-16 build (commit after `600170f`). TetherCell detects the installed binary and greys the probe fields out until it is installed.

---

## 3. TetherCell controls

| Control | Host flag / effect |
|---|---|
| Preset | Applies a group of values below |
| MTU | `-mtu` |
| Async reads | `-urbs` |
| Dead after | `-dead-after` |
| Wedge after | `-wedge-after` |
| Probe after | `-probe-after` (needs new build) |
| Probe every | `-probe-every` (needs new build) |
| GUI ping | TetherCell-only: `ping -I mxtun0 1.1.1.1` every N s; 0 = off. Shows RTT in the Link card. |
| Default route via tunnel | `-route=true/false` |
| ULA IPv6 + v6 route | `-v6=true/false` |
| One route | `-hostroute` |
| Drop root after setup | `-drop` |
| Interface | `-iface` |
| Serial | `-serial` |
| Attach to existing accessory | `-reuse` |
| Wait for accessory | `-wait` |
| CLEAR_HALT | `-clear-halt` |

Presets:

| Preset | MTU | Async reads | Probes |
|---|---|---|---|
| Max speed | 16000 | 8 | 3 s / 2 s |
| Max speed + deep async | 16000 | 16 | 3 s / 2 s |
| Old packet sizes (pre-v3) | 1400 | 8 | 3 s / 2 s |
| Debug (sync reads, slow probes) | 1400 | 0 | 8 s / 5 s |

Live panel: state (LIVE / EXTERNAL / STARTING / OFFLINE), interface addresses, phone USB mode, phone-app version, default route, throughput + totals from `/sys/class/net/<iface>/statistics`, latency, session age, and the host log with its 10-second `stats` line.

The "Command" box always shows the exact argv that Connect will run; **Copy** puts it on the clipboard.

---

## 4. Exit codes

| Code | From | Meaning |
|---|---|---|
| 0 | `up`, `run` | Stopped by you / clean shutdown |
| 1 | `up` | Not root, not installed, another tunnel already up, or gave up after 12 failed starts |
| 2 | `up`, `run` | Bad host flags (run `usbtether run -h`) |
| 3 | `run` → `up` | Tunnel died after working; supervisor reconnects in 2 s |
| 4 | `run` → `up` | Never delivered a packet; after 3 tries `up` asks for a replug |

---

## 5. What `setup` installs

| Artifact | Detail |
|---|---|
| `/usr/local/bin/usbtether` | root:root 0755, `cap_net_admin+ep` |
| `/etc/udev/rules.d/70-usbtether.rules` | `uaccess` for `04e8:*` and `18d1:2d0[0-5]` (replug once after install) |
| NetworkManager profile `usbtether` | TUN `mxtun0`, `10.77.0.1/24`, `fd77:7777::1/64`, DNS 1.1.1.1 / 8.8.8.8 + v6, `never-default`, DNS priority −100 |
| `~/.local/share/applications/usbtether.desktop` | "USB Tether" menu entry + Status action |
| `~/.local/share/applications/tethercell.desktop` | "TetherCell" GUI entry |

---

## 6. Phone side

No CLI. `com.matt.usbtether` is started by the USB-accessory attach broadcast or by `am start`, runs a `connectedDevice` foreground service, and its notification has a **Stop** action.

Commands the desktop issues when adb is authorized:

```
adb install -r <embedded.apk>
adb shell pm grant com.matt.usbtether android.permission.POST_NOTIFICATIONS
adb shell am start -n com.matt.usbtether/.MainActivity
adb shell am force-stop com.matt.usbtether      # only after a failed session
adb shell input keyevent KEYCODE_WAKEUP
```

---

## 7. Build, test, install

```bash
cd ~/work/usbtether
./dist.sh                # gomobile core → APK → embedded into the host binary, dist/
./dist.sh --skip-apk     # host only, reuse phone-app/build/usbtether.apk
(cd clone && go test ./internal/...)
dist/usbtether version
dist/usbtether setup     # one password popup, installs the build above
```

---

## 8. What is deliberately not a setting

**TTL.** The phone terminates every TCP flow in its gVisor netstack and re-dials it from its own sockets; ICMP echoes are answered by the phone itself (`pingV4`). IP headers from the desktop never reach the carrier, so a TTL set on the tunnel would be cosmetic. Changing the carrier-visible TTL is a phone-side root change — not available on this locked SM-S176V. Use the GUI ping interval if what you want is a health/latency signal.

---

## 9. Recipes

```bash
usbtether                                  # daily driver, max speed, auto-reconnect
usbtether up -hostroute 1.1.1.1/32 -route=false -v6=false   # one host through the tunnel
usbtether up -mtu 1400 -urbs 0             # A/B against pre-v3 packet sizes
usbtether up -probe-every 0                # no active probes (watches writes only)
usbtether status                           # what is installed / attached / routed
```

Same things in the GUI: pick a preset, adjust, **Connect**. The banner offers **Install update** when the installed binary is older than the GUI.
