package usbdev

import (
	"testing"
	"unsafe"
)

// Values printed by a C program against linux/usbdevice_fs.h (6.12, amd64).
func TestURBLayout(t *testing.T) {
	var u urb
	got := [6]uintptr{unsafe.Sizeof(u), unsafe.Offsetof(u.Status), unsafe.Offsetof(u.Flags),
		unsafe.Offsetof(u.Buffer), unsafe.Offsetof(u.ActualLength), unsafe.Offsetof(u.UserContext)}
	want := [6]uintptr{56, 4, 8, 16, 28, 48}
	if got != want {
		t.Fatalf("usbdevfs_urb layout %v, want %v", got, want)
	}
	if unsafe.Offsetof(u.BufferLength) != 24 {
		t.Fatal("buffer_length offset")
	}
}
