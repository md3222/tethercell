// Package tun creates and configures a Linux TUN device without cgo, using
// the /dev/net/tun ioctl, and configures addresses/routes via the ip(8)
// argv interface (never a shell).
package tun

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	tunSetIff  = 0x400454ca // _IOW('T', 202, int)
	iffTun     = 0x0001
	iffNoPi    = 0x1000
	iffTunExcl = 0x8000
)

// Device is an open TUN.
type Device struct {
	f    *os.File
	Name string
}

// Open opens (or creates) the named TUN. iffTunExcl makes the ioctl fail if
// the interface already exists, so we never attach to another process's TUN.
func Open(name string) (*Device, error) {
	if name == "" || len(name) >= 16 {
		return nil, fmt.Errorf("tun: invalid interface name %q", name)
	}
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("tun: open /dev/net/tun: %w", err)
	}
	var req [40]byte // struct ifreq: name[16], flags u16
	copy(req[:16], name)
	*(*uint16)(unsafe.Pointer(&req[16])) = iffTun | iffNoPi | iffTunExcl
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), tunSetIff, uintptr(unsafe.Pointer(&req[0])))
	if e != 0 {
		f.Close()
		return nil, fmt.Errorf("tun: TUNSETIFF %q: %w", name, e)
	}
	n := 0
	for n < 16 && req[n] != 0 {
		n++
	}
	return &Device{f: f, Name: string(req[:n])}, nil
}

// Read returns the next IP datagram (bare IP, no PI header).
func (d *Device) Read(p []byte) (int, error) { return d.f.Read(p) }

// Write injects an IP datagram.
func (d *Device) Write(p []byte) (int, error) { return d.f.Write(p) }

// Close destroys the interface.
func (d *Device) Close() error { return d.f.Close() }

func runIP(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %v: %w: %s", args, err, out)
	}
	return nil
}

// Configure brings the interface up and assigns addresses. If route is true,
// default routes through the device are installed (metric 1024, matching the
// daemon's tetrd0 setup) so the phone becomes the exit node.
func (d *Device) Configure(v4, v6 string, mtu int, route bool) error {
	cmds := [][]string{
		{"link", "set", "dev", d.Name, "mtu", strconv.Itoa(mtu)},
		{"link", "set", "dev", d.Name, "up"},
		{"addr", "replace", v4, "dev", d.Name},
		{"-6", "addr", "replace", v6, "dev", d.Name},
	}
	if route {
		cmds = append(cmds,
			[]string{"route", "replace", "default", "dev", d.Name, "proto", "static", "scope", "link", "metric", "1024"},
			[]string{"-6", "route", "replace", "default", "dev", d.Name, "metric", "1024"},
		)
	}
	for _, c := range cmds {
		if err := runIP(c...); err != nil {
			return err
		}
	}
	return nil
}
