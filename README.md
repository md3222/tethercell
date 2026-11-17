# tethercell

Own USB reverse tether: Linux desktop <-> Android phone over AOA (manufacturer `mxlab`, model `tethercell`).
The phone app (`com.matt.tethercell`) runs a gVisor netstack and re-dials every flow from its own sockets.

| dir | what |
|---|---|
| `clone/` | desktop: `tethercell` (setup/supervisor/host), usbfs AOA, async bulk-IN URBs, TUN `tcell0` |
| `phone-app/` | Android app (Java shell + gomobile Go core) |
| `gui/` | TetherCell: PyQt5 console for every host flag, live state/traffic/latency, 8 themes (View ▸ theme) |
| `docs/` | TetherCell reference — markdown + PDF (`build-pdf.py` regenerates it) |
| `dist.sh` | builds APK + desktop binary (APK embedded) into `dist/` |

Build: `./dist.sh` (or `--skip-apk`). Install: `dist/tethercell setup` (one password popup), then `tethercell`.
Signing key: `keys/tethercell.keystore` (NOT in git; back it up separately, a new key forces an app uninstall).
Host flags: `-mtu` (16000), `-urbs` (8, 0 = sync reads). Log + results: `PHONE-APP-PLAN.md`.
