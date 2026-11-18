//go:build windows

// Command tethercell on Windows — same CLI surface as the Linux build, same
// embedded phone app, but the transport is not written yet.
//
// What already works here: version, help, status, apk (extract the embedded
// APK for sideloading), and argument-compatible up/run that explain what is
// missing. The rest lands as the WinUSB/Wintun backends are filled in.
package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"tethercell/internal/host"
)

//go:embed assets/*
var assets embed.FS

var apk, _ = assets.ReadFile("assets/tethercell.apk")

var apkVersionRaw = func() string { b, _ := assets.ReadFile("assets/apk_version.txt"); return string(b) }()

var version = "dev" // -ldflags "-X main.version=..."

const helpText = `tethercell (Windows build)

  tethercell                 = tethercell up  (not implemented yet)
  tethercell up [flags]      supervisor; needs Wintun + WinUSB
  tethercell run [flags]     bare host; same requirement
  tethercell status          what this build can and cannot do
  tethercell apk [file]      write the embedded phone APK (works today)
  tethercell version

Not implemented on Windows yet: TUN (wintun.dll), USB (WinUSB), routes
(CreateIpForwardEntry2), DNS. The phone app and the AOA handshake are unchanged.

Flags are the same as Linux: -iface -mtu -urbs -wait -dead-after -wedge-after
-route -v6 -drop -clear-halt -serial -hostroute -reuse -probe-after -probe-every
`

func main() {
	cmd := "up"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "up", "run":
		host.Main(args)
	case "status":
		status()
	case "apk":
		out := "tethercell.apk"
		if len(args) > 0 {
			out = args[0]
		}
		if err := os.WriteFile(out, apk, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "tethercell:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (versionCode %d)\n", out, apkVersion())
	case "setup":
		fmt.Fprintln(os.Stderr, `tethercell: Windows setup is not written yet.
It will copy wintun.dll next to the exe and check the WinUSB binding (Zadig).
For now: bind WinUSB to the phone with Zadig, then run "tethercell".`)
		os.Exit(1)
	case "phone":
		fmt.Fprintln(os.Stderr, "tethercell: use \"adb install -r tethercell.apk\" (or 'tethercell apk' to write it out)")
		os.Exit(1)
	case "version", "-version", "--version":
		fmt.Printf("tethercell %s (windows), embedded phone app versionCode %d (%d bytes)\n", version, apkVersion(), len(apk))
	case "help", "-h", "--help":
		fmt.Print(helpText)
	default:
		fmt.Fprintln(os.Stderr, "tethercell: unknown command", cmd)
		os.Exit(2)
	}
}

func status() {
	row := func(k, v string) { fmt.Printf("%-14s %s\n", k, v) }
	row("binary", fmt.Sprintf("%s (windows, embedded app versionCode %d)", version, apkVersion()))
	row("transport", "NOT IMPLEMENTED: needs wintun.dll + WinUSB")
	row("phone", phoneState())
	row("next", "bind WinUSB with Zadig, then wait for the WinUSB backend")
}

func phoneState() string {
	out, err := exec.Command("adb", "devices").Output()
	if err != nil {
		return "adb not found (optional)"
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		if strings.Contains(line, "\tdevice") {
			return "adb sees " + strings.Fields(line)[0]
		}
	}
	return "no authorized device (allowed on Windows: no ADB driver needed)"
}

func apkVersion() int { n, _ := strconv.Atoi(strings.TrimSpace(apkVersionRaw)); return n }
