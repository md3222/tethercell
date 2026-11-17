# TetherCell — install

Own USB reverse tether: the phone becomes the desktop's uplink over a plain USB cable, no
rooting, no Wi-Fi, no hotspot. The desktop makes a TUN interface; the phone app runs a gVisor
network stack and re-dials every flow from its own sockets.

**What you need**

| | |
|---|---|
| Desktop | Linux x86-64, udev, sudo. No root process afterwards |
| Phone | Android 10+ (API 29), any phone that speaks AOA, USB **data** cable |
| Not needed | root, unlocked bootloader, developer options (except for `adb install`) |

## 1. Install

```sh
curl -fsSLO https://raw.githubusercontent.com/md3222/tethercell/main/release/install.sh
sh install.sh
```

That downloads the latest release, verifies `SHA256SUMS`, installs `/usr/local/bin/tethercell`
with `cap_net_admin`, the udev rule and a NetworkManager profile, and writes the phone APK to
`~/tethercell.apk`.

Pin a version with `sh install.sh v0.1.0`. Already have the files? `sh install.sh --local dist/`.

From a checkout instead:

```sh
./dist.sh                 # builds phone app + desktop binary into dist/
./dist/tethercell setup   # one password prompt
```

## 2. Phone app

```sh
adb install -r ~/tethercell.apk
```

No adb? Copy `~/tethercell.apk` to the phone (USB file transfer), open it in Files, allow
"install unknown apps" when prompted.

The app shows up as **TetherCell**. You do not have to open it: the desktop starts it when the
cable is plugged in.

## 3. First connect

Replug the cable once (the udev rule is new), then:

```sh
tethercell
```

Expected, in order:

```
tethercell: phone app versionCode 8 installed (embedded 8): ok
tethercell-host: AOA START sent (usb0 will drop); waiting for accessory
tethercell-host: accessory 18d1:2d01 in=0x81 out=0x1
tethercell-host: tun tcell0 up 10.77.0.1/24 (route=true)
```

`ip route` now shows `default dev tcell0`. Ctrl-C stops it and gives the routes back.

## 4. GUI (optional)

```sh
sudo apt install python3-pyqt5     # once
./gui/tethercell.py
```

Every host flag as a control, 8 themes under **View**, live traffic/latency, presets.

## 5. Tuning that actually matters

| Knob | Default | What it does |
|---|---|---|
| `-mtu` | 16000 | Bigger = fewer URBs = faster. Lower only if the phone drops packets |
| `-urbs` | 8 | Async bulk reads in flight. 16 helps on some hosts |
| `-wedge-after` | 6s | How long all USB writes may time out before the session is declared dead |
| `-probe-after` / `-probe-every` | 3s / 2s | First and periodic keepalive probe |
| `-v6` | on | IPv6 inside the tunnel |

`tethercell run -h` lists everything. GUI presets cover the common cases.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `no permission on /dev/bus/usb/...` | udev rule not active yet: replug, or run setup again |
| `another tunnel is already up (tcell0)` | another `tethercell`/`usbtether` is running: stop it (Ctrl-C, or GUI ▸ Tools) |
| `tunnel never came up` | the phone app did not open the accessory. Install the APK, unlock the phone, replug |
| Link up, no internet | carrier blocks tethering, or DNS: check `tethercell status`, try `ping 1.1.1.1` |
| `usb write timeout` spam | phone stopped reading: replug; check the app was not force-stopped |
| Another tether app steals the cable | close Tetrd/other AOA apps; TetherCell only claims `mxlab/tethercell` |

## Uninstall

```sh
sudo rm -f /usr/local/bin/tethercell /etc/udev/rules.d/70-tethercell.rules
sudo udevadm control --reload-rules
nmcli con delete tethercell 2>/dev/null
rm -f ~/.local/share/applications/tethercell*.desktop ~/tethercell.apk
```

Phone: uninstall **TetherCell** like any app.

## Notes

- **No carrier-side bypass.** The phone re-dials every flow from its own sockets, so the
  traffic looks like ordinary phone data. An account-level tethering block is unaffected.
- Original code (Go desktop, Java + gomobile phone app). Not affiliated with Tetrd; it only
  borrows the AOA idea, and claims its own accessory identity so the two never fight.
- The APK in the release is signed with a self-signed key, so it sideloads but will not update
  over a Play Store build.
- Linux only for the desktop half right now. A Windows port is planned; it needs WinUSB,
  Wintun and a driver-binding step, so it is not a rebuild of the same binary.
