
//go:build linux

package host

import (
	"fmt"
	"os"

	"tethercell/internal/tun"
)

// SelfTest checks the unprivileged path without the phone and without routes: create a scratch
// TUN, configure it via ip(8) with the ambient capability, drop every capability, confirm.
func SelfTest() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run as your user")
	}
	if !HasNetAdmin() {
		return fmt.Errorf("no CAP_NET_ADMIN: run the installed binary (tethercell setup)")
	}
	td, err := tun.Open("mxselftest0")
	if err != nil {
		return err
	}
	defer td.Close()
	for _, a := range [][]string{{"link", "set", "dev", td.Name, "mtu", "1400"}, {"addr", "add", "10.254.254.1/30", "dev", td.Name}, {"link", "set", "dev", td.Name, "up"}} {
		if out, err := sysCmd("ip", a...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %v: %v: %s", a, err, out)
		}
	}
	if err := DropAllCaps(); err != nil {
		return fmt.Errorf("drop: %w", err)
	}
	if err := sysCmd("ip", "link", "set", "dev", td.Name, "mtu", "1300").Run(); err == nil {
		return fmt.Errorf("ip still privileged after drop")
	}
	fmt.Println("selftest ok: TUN + ip(8) via ambient cap_net_admin, caps dropped and not regainable")
	return nil
}
