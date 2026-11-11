package usbdev

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// usbfs async URB ioctls (verified against linux/usbdevice_fs.h on 6.12 amd64, see async_test.go).
const (
	ioctlSubmitURB      = uintptr(0x8038550a) // _IOR('U', 10, struct usbdevfs_urb)
	ioctlDiscardURB     = uintptr(0x550b)     // _IO('U', 11)
	ioctlReapURBNoDelay = uintptr(0x4008550d) // _IOW('U', 13, void *)
	urbTypeBulk         = 3

	pollOut  = 0x4
	pollErr  = 0x8
	pollHup  = 0x10
	pollNval = 0x20
)

type pollFd struct {
	Fd      int32
	Events  int16
	Revents int16
}

// urb mirrors struct usbdevfs_urb (56 bytes on amd64, no iso descriptors).
type urb struct {
	Type         uint8
	Endpoint     uint8
	_            [2]byte
	Status       int32
	Flags        uint32
	_            [4]byte
	Buffer       uintptr
	BufferLength int32
	ActualLength int32
	StartFrame   int32
	NumPackets   int32
	ErrorCount   int32
	Signr        uint32
	UserContext  uintptr
}

// AsyncReader keeps n bulk-IN URBs queued so the host controller always has a
// buffer ready while the previous transfer is being parsed. URB structs and
// buffers live in one anonymous mmap, so the kernel never holds Go heap memory.
type AsyncReader struct {
	d      *Device
	mem    []byte
	urbs   []*urb
	bufs   [][]byte
	size   int
	queued int
}

// NewAsyncReader allocates n URBs of size bytes each and submits all of them.
// On error nothing is left queued.
func (d *Device) NewAsyncReader(n, size int) (*AsyncReader, error) {
	us := int(unsafe.Sizeof(urb{}))
	hdr := (n*us + 4095) &^ 4095
	mem, err := syscall.Mmap(-1, 0, hdr+n*size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("usbdev: mmap urbs: %w", err)
	}
	r := &AsyncReader{d: d, mem: mem, size: size}
	for i := 0; i < n; i++ {
		u := (*urb)(unsafe.Pointer(&mem[i*us]))
		b := mem[hdr+i*size : hdr+(i+1)*size : hdr+(i+1)*size]
		r.urbs = append(r.urbs, u)
		r.bufs = append(r.bufs, b)
		*u = urb{Type: urbTypeBulk, Endpoint: d.In, Buffer: uintptr(unsafe.Pointer(&b[0])),
			BufferLength: int32(size), UserContext: uintptr(i)}
	}
	for i := range r.urbs {
		if err := r.submit(i); err != nil {
			r.Close()
			return nil, err
		}
	}
	return r, nil
}

func (r *AsyncReader) submit(i int) error {
	u := r.urbs[i]
	u.Status, u.ActualLength = 0, 0
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, r.d.f.Fd(), ioctlSubmitURB, uintptr(unsafe.Pointer(u)))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return fmt.Errorf("usbdev: submit urb: %w", e)
		}
		r.queued++
		return nil
	}
}

// ErrIdle is returned by Next when no transfer completed within the timeout.
var ErrIdle = errors.New("usbdev: no transfer completed")

// Next waits up to timeout for the oldest queued transfer, calls fn with its
// data (valid only during fn), then resubmits that URB. Any URB error status
// is returned as an error (the caller tears the tunnel down; the supervisor reconnects).
func (r *AsyncReader) Next(timeout time.Duration, fn func([]byte)) error {
	u, err := r.reap(timeout)
	if err != nil {
		return err
	}
	i := int(u.UserContext)
	if u.Status != 0 {
		return fmt.Errorf("usbdev: bulk in urb: %w", syscall.Errno(-u.Status))
	}
	// ActualLength, never BufferLength: the same stale-tail bug af973df fixed in BulkRead.
	n := int(u.ActualLength)
	if n < 0 || n > r.size {
		return fmt.Errorf("usbdev: urb actual length %d out of range", n)
	}
	fn(r.bufs[i][:n])
	return r.submit(i)
}

// reap returns one completed URB, polling the fd so an idle link still wakes
// the caller every timeout. usbfs signals completions as POLLOUT.
func (r *AsyncReader) reap(timeout time.Duration) (*urb, error) {
	fd := int(r.d.f.Fd())
	deadline := time.Now().Add(timeout)
	for {
		var p uintptr
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlReapURBNoDelay, uintptr(unsafe.Pointer(&p)))
		switch e {
		case 0:
			r.queued--
			for _, u := range r.urbs { // map the kernel's pointer back to ours (also validates it)
				if uintptr(unsafe.Pointer(u)) == p {
					return u, nil
				}
			}
			return nil, fmt.Errorf("usbdev: reaped unknown urb %#x", p)
		case syscall.EINTR:
			continue
		case syscall.EAGAIN:
		default:
			return nil, fmt.Errorf("usbdev: reap urb: %w", e)
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, ErrIdle
		}
		pfd := pollFd{Fd: int32(fd), Events: pollOut}
		_, _, pe := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(left/time.Millisecond)+1)
		if pe != 0 && pe != syscall.EINTR {
			return nil, fmt.Errorf("usbdev: poll: %w", pe)
		}
		if pfd.Revents&(pollErr|pollHup|pollNval) != 0 {
			return nil, fmt.Errorf("usbdev: device gone (poll revents %#x): %w", pfd.Revents, syscall.ENODEV)
		}
	}
}

// Close discards every queued URB and reaps them back (bounded), then frees
// the buffers only if all came back; otherwise the mapping is leaked on purpose
// (closing the fd later releases the URBs).
func (r *AsyncReader) Close() {
	fd := r.d.f.Fd()
	for _, u := range r.urbs {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlDiscardURB, uintptr(unsafe.Pointer(u)))
	}
	deadline := time.Now().Add(time.Second)
	for r.queued > 0 && time.Now().Before(deadline) {
		var p uintptr
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlReapURBNoDelay, uintptr(unsafe.Pointer(&p)))
		switch e {
		case 0:
			r.queued--
		case syscall.EAGAIN, syscall.EINTR:
			time.Sleep(5 * time.Millisecond)
		default:
			r.queued = -1 // device gone: kernel frees on fd close, keep the mapping
		}
	}
	if r.queued == 0 {
		_ = syscall.Munmap(r.mem)
	}
	r.mem = nil
}
