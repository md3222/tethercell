
//go:build linux

package host

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"tethercell/internal/usbdev"
)

// Unprivileged mode: the installed binary carries the file capability cap_net_admin+ep
// (tethercell setup) and udev gives the logged-in user the phone's USB node, so no root
// process exists at all. ip(8) children get CAP_NET_ADMIN as an ambient capability.

const capNetAdmin = 12

// HasNetAdmin reports whether CAP_NET_ADMIN is in this process's effective set.
func HasNetAdmin() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && n&(1<<capNetAdmin) != 0
		}
	}
	return false
}

// sysCmd builds an argv command (never a shell) that inherits CAP_NET_ADMIN when we are not root.
func sysCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	if os.Geteuid() != 0 && HasNetAdmin() { // after DropAllCaps (e.g. failover's nmcli) run plain
		c.SysProcAttr = &syscall.SysProcAttr{AmbientCaps: []uintptr{capNetAdmin}}
	}
	return c
}

type capHeader struct {
	version uint32
	pid     int32
}
type capData struct{ effective, permitted, inheritable uint32 }

// DropAllCaps clears every capability on every thread and sets no_new_privs. Needs CGO_ENABLED=0
// (syscall.AllThreadsSyscall). Exec'ing the file-capability binary again re-grants them, which is
// how the supervisor starts a fresh host after a reconnect.
func DropAllCaps() error { return dropCaps(true) }

// DropCapsKeepExec clears all capabilities on every thread but leaves no_new_privs unset, so a
// later exec of the file-capability binary (the next host run) gets cap_net_admin again.
func DropCapsKeepExec() error { return dropCaps(false) }

func dropCaps(nnp bool) error {
	hdr := capHeader{version: 0x20080522}
	var data [2]capData
	if _, _, e := syscall.AllThreadsSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return e
	}
	if !nnp {
		if HasNetAdmin() {
			return errors.New("CAP_NET_ADMIN still effective after drop")
		}
		return nil
	}
	if _, _, e := syscall.AllThreadsSyscall6(syscall.SYS_PRCTL, 38 /*PR_SET_NO_NEW_PRIVS*/, 1, 0, 0, 0, 0); e != 0 {
		return e
	}
	if HasNetAdmin() {
		return errors.New("CAP_NET_ADMIN still effective after drop")
	}
	return nil
}

// openRetry opens a freshly enumerated device; udev applies the uaccess ACL a moment after the
// node appears, so EACCES right after re-enumeration is retried for a few seconds.
func openRetry(info usbdev.Info, claim bool) (*usbdev.Device, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := usbdev.Open(info, claim)
		if err == nil || !errors.Is(err, os.ErrPermission) || time.Now().After(deadline) {
			if err != nil && errors.Is(err, os.ErrPermission) {
				log.Printf("no permission on %s: run 'tethercell setup' (installs the udev rule)", info.Path())
			}
			return d, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}
