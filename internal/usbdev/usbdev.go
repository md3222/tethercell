// Package usbdev talks to USB devices through Linux usbfs, without libusb or
// cgo. It implements just what the Tetrd AOA handshake and the accessory bulk
// pipe need.
package usbdev

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	// _IOWR('U', 0, struct usbdevfs_ctrltransfer)   (24-byte struct)
	ioctlControl = uintptr(0xC0185500)
	// _IOWR('U', 2, struct usbdevfs_bulktransfer)   (24-byte struct)
	ioctlBulk = uintptr(0xC0185502)
	// _IOR('U', 15, unsigned int)
	ioctlClaimInterface = uintptr(0x8004550F)
	// _IOR('U', 16, unsigned int)
	ioctlReleaseInterface = uintptr(0x80045510)
	// USBDEVFS_CLEAR_HALT = _IOR('U', 21, unsigned int)
	ioctlClearHalt = uintptr(0x80045515)

	aoaGetProtocol = 51
	aoaSendString  = 52
	aoaStart       = 53

	// Google accessory mode product IDs.
	GoogleVID = 0x18d1
)

// AOA PIDs we accept as "accessory is up".
var accessoryPIDs = map[uint16]bool{
	0x2d00: true, // accessory
	0x2d01: true, // accessory + adb
	0x2d02: true, // accessory + audio
	0x2d03: true, // accessory + audio + adb
	0x2d04: true, // accessory + audio
	0x2d05: true, // all
}

type ctrlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32
	Data        uintptr
}

type bulkTransfer struct {
	Ep      uint32
	Len     uint32
	Timeout uint32
	Data    uintptr
}

// Info is one USB device as seen in sysfs.
type Info struct {
	SysDir  string // e.g. /sys/bus/usb/devices/1-1
	Bus     int
	Dev     int
	VID     uint16
	PID     uint16
	Serial  string
	Product string
}

// Path returns the usbfs node path.
func (i Info) Path() string {
	return fmt.Sprintf("/dev/bus/usb/%03d/%03d", i.Bus, i.Dev)
}

// List enumerates USB devices (not interfaces).
func List() ([]Info, error) {
	dirs, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, d := range dirs {
		vidb, err := os.ReadFile(filepath.Join(d, "idVendor"))
		if err != nil {
			continue
		}
		pidb, err := os.ReadFile(filepath.Join(d, "idProduct"))
		if err != nil {
			continue
		}
		vid, err1 := strconv.ParseUint(strings.TrimSpace(string(vidb)), 16, 16)
		pid, err2 := strconv.ParseUint(strings.TrimSpace(string(pidb)), 16, 16)
		if err1 != nil || err2 != nil {
			continue
		}
		busb, _ := os.ReadFile(filepath.Join(d, "busnum"))
		devb, _ := os.ReadFile(filepath.Join(d, "devnum"))
		bus, _ := strconv.Atoi(strings.TrimSpace(string(busb)))
		dev, _ := strconv.Atoi(strings.TrimSpace(string(devb)))
		if bus == 0 || dev == 0 {
			continue
		}
		ser, _ := os.ReadFile(filepath.Join(d, "serial"))
		prod, _ := os.ReadFile(filepath.Join(d, "product"))
		out = append(out, Info{
			SysDir: d, Bus: bus, Dev: dev, VID: uint16(vid), PID: uint16(pid),
			Serial: strings.TrimSpace(string(ser)), Product: strings.TrimSpace(string(prod)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path() < out[j].Path() })
	return out, nil
}

// Find returns the first device matching vid:pid, if pid is 0 any PID matches.
func Find(vid, pid uint16) (Info, bool) {
	all, err := List()
	if err != nil {
		return Info{}, false
	}
	for _, d := range all {
		if d.VID == vid && (pid == 0 || d.PID == pid) {
			return d, true
		}
	}
	return Info{}, false
}

// WaitAccessory polls for a Google accessory device.
func WaitAccessory(timeout time.Duration) (Info, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		all, err := List()
		if err == nil {
			for _, d := range all {
				if d.VID == GoogleVID && accessoryPIDs[d.PID] {
					return d, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Info{}, fmt.Errorf("usbdev: no accessory device within %s", timeout)
}

// Device is an open usbfs handle.
type Device struct {
	Info Info
	f    *os.File
	In   uint8 // bulk IN endpoint (phone -> host)
	Out  uint8 // bulk OUT endpoint (host -> phone)
}

// Open opens the device and (if claim is true) claims interface 0 and locates
// the bulk endpoints.
func Open(info Info, claim bool) (*Device, error) {
	f, err := os.OpenFile(info.Path(), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{Info: info, f: f}
	if claim {
		if err := d.Claim(0); err != nil {
			f.Close()
			return nil, err
		}
		if err := d.findEndpoints(0); err != nil {
			d.Release(0)
			f.Close()
			return nil, err
		}
	}
	return d, nil
}

// Claim claims a USB interface.
func (d *Device) Claim(iface uint32) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlClaimInterface, uintptr(unsafe.Pointer(&iface)))
	if e != 0 {
		return fmt.Errorf("usbdev: claim interface %d: %w", iface, e)
	}
	return nil
}

// Release releases a USB interface.
func (d *Device) Release(iface uint32) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlReleaseInterface, uintptr(unsafe.Pointer(&iface)))
}

// ClearHalt clears a halt/stall on endpoint ep and resets its data toggle on both sides.
func (d *Device) ClearHalt(ep uint8) error {
	v := uint32(ep)
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlClearHalt, uintptr(unsafe.Pointer(&v)))
	if e != 0 {
		return fmt.Errorf("usbdev: clear halt %#x: %w", ep, e)
	}
	return nil
}

// findEndpoints locates the bulk IN/OUT pair belonging to interface iface.
// An AOA+ADB device (18d1:2d01) exposes two ff interfaces, each with its own
// bulk pair (0x01/0x81 for the accessory, 0x02/0x82 for ADB), so the interface
// number must be honoured.
func (d *Device) findEndpoints(iface uint32) error {
	ifdir := ""
	subdirs, _ := filepath.Glob(filepath.Join(d.Info.SysDir, "*"))
	for _, sub := range subdirs {
		nb, err := os.ReadFile(filepath.Join(sub, "bInterfaceNumber"))
		if err != nil {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(nb)), 16, 8)
		if err != nil || uint32(n) != iface {
			continue
		}
		ifdir = sub
		break
	}
	if ifdir == "" {
		return fmt.Errorf("usbdev: interface %d not found in sysfs", iface)
	}
	eps, _ := filepath.Glob(filepath.Join(ifdir, "ep_*"))
	for _, ep := range eps {
		addrb, err := os.ReadFile(filepath.Join(ep, "bEndpointAddress"))
		if err != nil {
			continue
		}
		addr64, err := strconv.ParseUint(strings.TrimSpace(string(addrb)), 16, 8)
		if err != nil {
			continue
		}
		addr := uint8(addr64)
		// Prefer bulk; accessory interfaces only expose bulk, but be strict
		// when bmAttributes is readable.
		if attrb, err := os.ReadFile(filepath.Join(ep, "bmAttributes")); err == nil {
			if a, err := strconv.ParseUint(strings.TrimSpace(string(attrb)), 16, 8); err == nil {
				if a&0x03 != 0x02 {
					continue
				}
			}
		}
		if addr&0x80 != 0 {
			if d.In == 0 {
				d.In = addr
			}
		} else if d.Out == 0 {
			d.Out = addr
		}
	}
	if d.In == 0 || d.Out == 0 {
		return fmt.Errorf("usbdev: bulk endpoints not found (in=%#x out=%#x)", d.In, d.Out)
	}
	return nil
}

// Control performs a control transfer. For IN transfers data is filled in and
// its used prefix returned; for OUT transfers data is sent.
func (d *Device) Control(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) ([]byte, error) {
	var ptr uintptr
	if len(data) > 0 {
		ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	t := ctrlTransfer{
		RequestType: reqType,
		Request:     req,
		Value:       value,
		Index:       index,
		Length:      uint16(len(data)),
		Timeout:     uint32(timeout / time.Millisecond),
		Data:        ptr,
	}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlControl, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return nil, fmt.Errorf("usbdev: control %#x/%d: %w", reqType, req, e)
	}
	if int(t.Length) <= len(data) {
		return data[:t.Length], nil
	}
	return data, nil
}

// IsAccessory reports whether info is a Google AOA accessory.
func IsAccessory(info Info) bool {
	return info.VID == GoogleVID && accessoryPIDs[info.PID]
}

// BulkRead reads up to len(buf) bytes from the IN endpoint.
func (d *Device) BulkRead(buf []byte, timeout time.Duration) (int, error) {
	t := bulkTransfer{
		Ep:      uint32(d.In),
		Len:     uint32(len(buf)),
		Timeout: uint32(timeout / time.Millisecond),
		Data:    uintptr(unsafe.Pointer(&buf[0])),
	}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlBulk, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return 0, fmt.Errorf("usbdev: bulk in: %w", e)
	}
	return int(t.Len), nil
}

// BulkWrite writes to the OUT endpoint.
func (d *Device) BulkWrite(data []byte, timeout time.Duration) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	t := bulkTransfer{
		Ep:      uint32(d.Out),
		Len:     uint32(len(data)),
		Timeout: uint32(timeout / time.Millisecond),
		Data:    uintptr(unsafe.Pointer(&data[0])),
	}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlBulk, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return 0, fmt.Errorf("usbdev: bulk out: %w", e)
	}
	return int(t.Len), nil
}

// Close closes the handle.
func (d *Device) Close() error { return d.f.Close() }

// GetProtocol performs AOA GET_PROTOCOL.
func (d *Device) GetProtocol() (uint16, error) {
	buf := make([]byte, 2)
	if _, err := d.Control(0xC0, aoaGetProtocol, 0, 0, buf, 2*time.Second); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf), nil
}

// SendString performs AOA SEND_STRING for index.
func (d *Device) SendString(index uint16, s string) error {
	buf := append([]byte(s), 0)
	_, err := d.Control(0x40, aoaSendString, 0, index, buf, 2*time.Second)
	return err
}

// StartAccessory performs AOA START.
func (d *Device) StartAccessory() error {
	_, err := d.Control(0x40, aoaStart, 0, 0, nil, 2*time.Second)
	return err
}
