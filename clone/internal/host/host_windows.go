//go:build windows

// Windows host. The Linux host owns the TUN, the routes and the usbfs pump;
// none of that exists on Windows yet, so this file keeps the same entry points
// and fails with a message that names the missing piece instead of pretending.
package host

import (
	"flag"
	"fmt"
	"os"
)

// Main is the `tethercell run` entry point. It parses the same flag set as the
// Linux host (so the GUI and scripts see identical arguments) and then reports
// what is still missing.
func Main(args []string) {
	fs := flag.NewFlagSet("tethercell run", flag.ExitOnError)
	iface := fs.String("iface", "TetherCell", "Wintun adapter name to create/use")
	mtu := fs.Int("mtu", 16000, "tunnel MTU")
	urbs := fs.Int("urbs", 8, "async USB reads in flight")
	route := fs.Bool("route", true, "install a default route through the tunnel")
	v6 := fs.Bool("v6", true, "enable IPv6 inside the tunnel")
	_ = fs.Parse(args)

	fmt.Fprintf(os.Stderr, `tethercell: Windows transport not implemented.

  parsed:  iface=%q mtu=%d urbs=%d route=%v v6=%v
  missing: wintun.dll (tun adapter) and WinUSB binding for the phone
           (Zadig: bind WinUSB to the phone, then allow the accessory prompt)

The same phone app works unchanged: AOA accessory 18d1:2d01, mxlab/tethercell.
`, *iface, *mtu, *urbs, *route, *v6)
	os.Exit(4) // same code as "tunnel never came up" on Linux
}

// HasNetAdmin reports whether the process may change routes. On Windows that is
// simply "is this process elevated".
func HasNetAdmin() bool { return false }

// DropAllCaps / DropCapsKeepExec are Linux capability plumbing; nothing to drop
// on Windows (the process is either elevated or it is not).
func DropAllCaps() error     { return nil }
func DropCapsKeepExec() error { return nil }

// SelfTest exercises the parts that can be exercised without a driver.
func SelfTest() error {
	return fmt.Errorf("selftest: Windows backend not implemented (wintun.dll + WinUSB missing)")
}
