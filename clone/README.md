# tetrd-clone — desktop-side interop daemon

An **original** Go implementation of the desktop half of the Tetrd 1.3.2
protocol, written to interoperate with the existing phone app (which is left
untouched). It does not bundle or repackage any Tetrd binary or asset.

Protocol reference: `../REA-2026-10-31.md`.

## What it does

1. Puts the phone into Android Open Accessory mode (AOA control requests
   51/52/53, identity `robskie` / `tether` / `Tetrd USB Accessory` / `1.0` /
   `https://www.tetrd.app`, serial = unpadded base64 of
   `"1926590271\nSAMSUNG_Android\n<unix_ms>"`).
2. Speaks the decoded RPC control ladder over the accessory bulk pipe
   (`Version`, `SetLogLevelV2`, `ConfigV2`, `ConnectDeviceV2`, `ConnectV2`,
   `OnDevice*`, `AuthorizeDeviceV2`, `CurrentTimeV2`, keepalives). Captured
   `config.ConfigV2` / `device.NetConfigV2` / `device.InfoV2` blobs are replayed
   with only the sequence number rewritten.
3. Creates a host TUN (`tetrd0`) at `10.0.3.1/24` + `2001:feed:beef::1/64` and
   moves bare IPv4/IPv6 datagrams over USB bulk.

## Layout

| Package | Role |
|---|---|
| `internal/wire` | frame split/encode, TLV value codec, `SetSeq`, `IPLength` |
| `internal/usbdev` | Linux usbfs ioctls (no libusb/cgo) + AOA handshake |
| `internal/proto` | RPC peer loop + the connect/authorize ladder |
| `internal/tun` | `/dev/net/tun` ioctl + `ip(8)` argv configuration |
| `cmd/tetrd-clone` | main entry point |

## Build & test

```
go build ./...
go vet ./...
go test ./...
```

The unit test re-splits all 1100 captured RPC frames in `fixtures/frames.jsonl`
and round-trips sequence-number rewriting on every captured fixture.

## Run

Needs root (usbfs bulk ioctls + `TUNSETIFF`). Stop the real daemon first:

```
sudo-gui systemctl stop tetrd          # or however it is managed
sudo-gui /home/matt/work/tetrd-re/clone/bin/tetrd-clone -route
```

`-route` installs default routes through the TUN (metric 1024) so the phone
becomes the exit node. Leave it off to bring the link up without touching the
routing table.

If the phone is already in accessory mode (`18d1:2d01`), the clone attaches
directly; otherwise it drives the AOA switch (`-aoa`, default on) and waits for
re-enumeration (`-wait`, default 15s).

## Not implemented / open

- Reverse/phone-addressed traffic semantics beyond a plain bridge (§6 of the
  report) — the tunnel is a straight TUN ⇄ USB bridge.
- No `nmcli`/resolvconf DNS management; `/etc/resolv.conf` is left alone.
- Live end-to-end test still pending: stopping the running daemon drops the
  desktop's current tether path, so it needs an agreed window.
