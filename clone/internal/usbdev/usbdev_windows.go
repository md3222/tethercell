//go:build windows

// Windows USB transport. Skeleton for the port; the Linux side uses usbfs
// ioctls, which do not exist here.
//
// The real backend is WinUSB:
//
//	bind    Zadig (or an INF) attaches WinUSB to the phone's interface; the
//	        AOA control requests (51/52/53) go out on endpoint 0
//	open    CreateFile(`\\?\usb#vid_18d1&pid_2d01#...`) + WinUsb_Initialize
//	reads   WinUsb_ReadPipe with an OVERLAPPED per queued URB (async)
//	writes  WinUsb_WritePipe, one transfer at a time
//
// Device identity is the same AOA accessory (18d1:2d01, mxlab/tethercell) as on
// Linux, so the phone app does not change.
package usbdev

import (
	"errors"
	"time"
)

var (
	// ErrIdle means no transfer completed within the caller's patience.
	ErrIdle = errors.New("usbdev: no transfer completed")
	// ErrNotImplemented gates every entry point until WinUSB lands.
	ErrNotImplemented = errors.New("usbdev: Windows backend not implemented (needs WinUSB binding + WinUsb_ReadPipe)")
)

// Info describes a candidate device the way the Linux backend reports it.
type Info struct {
	Bus, Dev   int
	Vendor, Product uint16
	Serial     string
	Path       string
	Interfaces []int
}

func List() ([]Info, error)                   { return nil, ErrNotImplemented }
func Find(vid, pid uint16) (Info, bool)       { return Info{}, false }
func WaitAccessory(timeout time.Duration) (Info, error) {
	return Info{}, ErrNotImplemented
}
func IsAccessory(info Info) bool { return info.Vendor == 0x18d1 && (info.Product&0xfff0) == 0x2d00 }

type Device struct{ info Info }

func Open(info Info, claim bool) (*Device, error) { return nil, ErrNotImplemented }

func (d *Device) Control(reqType, req uint8, value, index uint16, data []byte) (int, error) {
	return 0, ErrNotImplemented
}
func (d *Device) BulkOut(data []byte) (int, error) { return 0, ErrNotImplemented }
func (d *Device) BulkIn(buf []byte, timeout time.Duration) (int, error) {
	return 0, ErrNotImplemented
}
func (d *Device) ClearHalt(endpoint uint8) error { return ErrNotImplemented }
func (d *Device) Close() error                   { return nil }

// AsyncReader mirrors the Linux async bulk-IN API: N transfers in flight,
// returned as they complete.
type AsyncReader struct {
	dev    *Device
	queued int
}

func NewAsyncReader(dev *Device, bufSize, queued int) (*AsyncReader, error) {
	return &AsyncReader{dev: dev, queued: queued}, ErrNotImplemented
}

func (r *AsyncReader) Next(buf []byte) (int, error) { return 0, ErrNotImplemented }
func (r *AsyncReader) Close() error                 { return nil }
