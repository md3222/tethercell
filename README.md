# usbtether

Own USB reverse tether: Linux desktop <-> Android phone over AOA (manufacturer `mxlab`, model `usbtether`).
The phone app (`com.matt.usbtether`) runs a gVisor netstack and re-dials every flow from its own sockets.

| dir | what |
|---|---|
| `clone/` | desktop: `usbtether` (setup/supervisor/host), usbfs AOA, async bulk-IN URBs, TUN `mxtun0` |
| `phone-app/` | Android app (Java shell + gomobile Go core) |
| `dist.sh` | builds APK + desktop binary (APK embedded) into `dist/` |

Build: `./dist.sh` (or `--skip-apk`). Install: `dist/usbtether setup` (one password popup), then `usbtether`.
Signing key: `keys/usbtether.keystore` (NOT in git; back it up separately, a new key forces an app uninstall).
Host flags: `-mtu` (16000), `-urbs` (8, 0 = sync reads). Log + results: `PHONE-APP-PLAN.md`.
