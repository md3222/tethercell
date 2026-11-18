//go:build windows

// Windows TUN backend. This is the skeleton the port fills in:
//
//	create   WintunCreateAdapter(L"tethercell", L"TetherCell", NULL)  (wintun.dll)
//	up       adapter session + SetIPAddresses/SetDNS via Win32 or `netsh`
//	routes   CreateIpForwardEntry2 with metric < the Wi-Fi metric
//	teardown WintunCloseAdapter / delete the routes
//
// Nothing here pretends to work: Open returns ErrNotImplemented so the CLI can
// say exactly what is missing instead of half-starting a tunnel.
package tun

import (
	"errors"
	"io"
)

// ErrNotImplemented is returned by every stub until the real backend lands.
var ErrNotImplemented = errors.New("tun: Windows backend not implemented (needs Wintun: wintun.dll + WintunCreateAdapter/WintunStartSession)")

type Device struct {
	Name  string
	addrs []string
}

func Open(name string) (*Device, error) { return nil, ErrNotImplemented }

func (d *Device) Read(p []byte) (int, error)  { return 0, io.EOF }
func (d *Device) Write(p []byte) (int, error) { return 0, io.EOF }
func (d *Device) Close() error                { return nil }

// Addrs reports the addresses the backend assigned to the adapter.
func (d *Device) Addrs() []string { return d.addrs }
