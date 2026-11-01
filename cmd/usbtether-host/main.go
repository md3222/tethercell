// Command usbtether-host is the desktop half of Matt's own USB tether: it
// switches the phone into Android Open Accessory mode with OUR identity
// (mxlab/usbtether, not Tetrd's), creates a TUN, and moves bare IP datagrams
// over USB bulk. Not Tetrd-protocol-compatible by design.
//
// Safety: no routes are installed unless asked. Default is bring-up only;
// use -hostroute <ip>/32 for a one-destination test, -route for a default route.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tetrd-clone/internal/tun"
	"tetrd-clone/internal/usbdev"
)

const (
	samsungVID = 0x04e8
	hostAddr   = "10.77.0.1/24"
	mtu        = 1400
)

func main() {
	iface := flag.String("iface", "mxtun0", "TUN interface name (never tetrd0)")
	hostRoute := flag.String("hostroute", "", "install ONE route, e.g. 1.1.1.1/32, via the TUN")
	route := flag.Bool("route", false, "install a default route via the TUN (metric 50: wins over wlan0)")
	serial := flag.String("serial", "", "USB serial of the phone in normal mode")
	drop := flag.Bool("drop", true, "drop root to SUDO_UID after the TUN/USB fds are open and routes installed")
	v6 := flag.Bool("v6", false, "also give the TUN a ULA IPv6 address (and a v6 default route with -route)")
	deadAfter := flag.Duration("dead-after", 15*time.Second, "declare the tunnel dead if data was sent and nothing came back for this long")
	wedgeAfter := flag.Duration("wedge-after", 6*time.Second, "declare the tunnel dead if every USB write has timed out for this long")
	reuse := flag.Bool("reuse", false, "attach to a phone already in accessory mode (our identity) instead of requiring a replug")
	probe := flag.String("probe-profile", "", "DIAGNOSTIC: 'tetrd' sends the stock app identity to test whether the phone switches at all")
	wait := flag.Duration("wait", 20*time.Second, "wait for the accessory to enumerate")
	flag.Parse()
	log.SetPrefix("usbtether-host: ")
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if os.Geteuid() != 0 {
		log.Fatal("must run as root (usbfs + TUNSETIFF)")
	}
	probeProfile = *probe
	reuseAcc = *reuse
	dev, err := acquire(*serial, *wait)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { dev.Release(0); dev.Close() }()
	log.Printf("accessory %04x:%04x in=%#x out=%#x", dev.Info.VID, dev.Info.PID, dev.In, dev.Out)

	td, err := tun.Open(*iface)
	if err != nil {
		log.Fatal(err)
	}
	defer td.Close()
	// Keep NetworkManager's hands off (it stripped nothing proven, but never let it guess).
	// If an NM profile "usbtether" (type tun) exists, hand the interface to NM *before* we add routes:
	// NM then owns addresses + DNS (so DNS survives Wi-Fi going away) and reports "connected" (browsers
	// stay online). Activation re-applies the device and wipes foreign routes, hence routes come last.
	if out, err := exec.Command("nmcli", "-g", "connection.id", "con", "show", "usbtether").Output(); err == nil && len(out) > 0 {
		_ = exec.Command("nmcli", "dev", "set", td.Name, "managed", "yes").Run()
		if o, err := exec.Command("nmcli", "con", "up", "usbtether", "ifname", td.Name).CombinedOutput(); err != nil {
			log.Printf("nmcli con up usbtether: %v: %s (continuing without NM)", err, o)
		} else {
			log.Printf("NetworkManager adopted %s (profile usbtether: DNS via tunnel)", td.Name)
		}
	} else {
		_ = exec.Command("nmcli", "dev", "set", td.Name, "managed", "no").Run()
	}

	must := func(args ...string) {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			log.Fatalf("ip %v: %v: %s", args, err, out)
		}
	}
	must("link", "set", "dev", td.Name, "mtu", fmt.Sprint(mtu))
	must("addr", "replace", hostAddr, "dev", td.Name)
	must("link", "set", "dev", td.Name, "up")
	if *v6 {
		must("-6", "addr", "replace", "fd77:7777::1/64", "dev", td.Name, "nodad")
	}
	out, _ := exec.Command("ip", "-4", "-o", "addr", "show", "dev", td.Name).Output()
	if !strings.Contains(string(out), "10.77.0.1/24") {
		log.Fatalf("IPv4 address missing after bring-up: %q", out)
	}
	switch {
	case *route:
		must("route", "replace", "default", "dev", td.Name, "metric", "50")
		if *v6 {
			must("-6", "route", "replace", "default", "dev", td.Name, "metric", "50")
		}
	case *hostRoute != "":
		must("route", "replace", *hostRoute, "dev", td.Name)
	}
	log.Printf("tun %s up %s (route=%v hostroute=%q)", td.Name, hostAddr, *route, *hostRoute)

	if *drop {
		if err := dropPrivileges(); err != nil {
			log.Fatalf("drop privileges: %v", err)
		}
	}

	stop := make(chan struct{})
	var once sync.Once
	quit := func() { once.Do(func() { close(stop) }) }
	errc := make(chan error, 2)
	go func() { errc <- usbToTun(dev, td, stop) }()
	go func() { errc <- tunToUSB(dev, td, stop) }()

	go func() { // watchdog: data out but nothing back for too long = dead tunnel, give routes back
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			if t := unanswered.Load(); t != 0 && time.Since(time.Unix(0, t)) > *deadAfter {
				errc <- fmt.Errorf("no data from phone for %s while sending", *deadAfter)
				return
			}
			if t := wedgedSince.Load(); t != 0 && time.Since(time.Unix(0, t)) > *wedgeAfter {
				errc <- fmt.Errorf("USB writes to the phone failing for %s (phone stopped reading)", *wedgeAfter)
				return
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	abnormal := false
	select {
	case s := <-sig:
		log.Printf("signal %v", s)
	case err := <-errc:
		log.Printf("pump ended: %v", err)
		abnormal = true
	}
	quit()
	td.Close() // interface + routes vanish now, so Wi-Fi/usb0 take over immediately
	if abnormal {
		failover()
	}
	time.Sleep(700 * time.Millisecond) // let read loops notice stop
}

// failover runs after an unexpected tunnel death: if Wi-Fi was switched off for the tunnel, bring it back.
func failover() {
	out, _ := exec.Command("nmcli", "-t", "-f", "DEVICE,STATE", "dev").Output()
	if !strings.Contains(string(out), "wlan0:connected") {
		log.Printf("failover: tunnel died, reconnecting wlan0")
		_ = exec.Command("nmcli", "radio", "wifi", "on").Run()
		_ = exec.Command("nmcli", "dev", "connect", "wlan0").Run()
	}
}

func ipLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	switch b[0] >> 4 {
	case 4:
		if len(b) < 4 {
			return 0
		}
		return int(b[2])<<8 | int(b[3])
	case 6:
		if len(b) < 6 {
			return 0
		}
		return 40 + (int(b[4])<<8 | int(b[5]))
	}
	return -1
}

func usbToTun(d *usbdev.Device, td *tun.Device, stop <-chan struct{}) error {
	rd := make([]byte, 16384)
	var acc []byte
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		n, err := d.BulkRead(rd, 500*time.Millisecond)
		if err != nil {
			if errors.Is(err, syscall.ETIMEDOUT) {
				continue
			}
			return err
		}
		unanswered.Store(0)
		acc = append(acc, rd[:n]...)
		for {
			for len(acc) > 0 && acc[0] == 0 { // 0x00 = padding (keeps transfers off 512-byte multiples)
				acc = acc[1:]
			}
			l := ipLen(acc)
			if l < 0 {
				acc = acc[:0]
				break
			}
			if l == 0 || l > len(acc) {
				break
			}
			if _, err := td.Write(acc[:l]); err != nil {
				log.Printf("tun write: %v", err)
			}
			acc = acc[l:]
		}
	}
}

func tunToUSB(d *usbdev.Device, td *tun.Device, stop <-chan struct{}) error {
	ch := make(chan []byte, 512)
	errc := make(chan error, 1)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := td.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			if n > 0 {
				ch <- append([]byte(nil), buf[:n]...)
			}
		}
	}()
	out := make([]byte, 0, 16384)
	wrRun, wrDropped := 0, 0
	var pending []byte
	for {
		var first []byte
		if pending != nil {
			first, pending = pending, nil
		} else {
			select {
			case <-stop:
				return nil
			case err := <-errc:
				return err
			case first = <-ch:
			}
		}
		out = append(out[:0], first...)
	drain:
		for len(out) < 15000 {
			select {
			case p := <-ch:
				if len(out)+len(p) > 16383 { // phone reads into 16384; keep room for the pad byte
					pending = p
					break drain
				}
				out = append(out, p...)
			default:
				break drain
			}
		}
		if len(out)%512 == 0 {
			out = append(out, 0) // avoid needing a ZLP to terminate the transfer
		}
		if _, err := d.BulkWrite(out, 2*time.Second); err != nil {
			unanswered.CompareAndSwap(0, time.Now().UnixNano())
			if errors.Is(err, syscall.ETIMEDOUT) {
				// phone not reading: drop, TCP retransmits. Track the run so the watchdog sees a wedged OUT pipe.
				if wedgedSince.Load() == 0 {
					wedgedSince.Store(time.Now().UnixNano())
				}
				wrRun++
				wrDropped += len(out)
				log.Printf("usb write timeout #%d (dropped %d bytes, %d this run)", wrRun, len(out), wrDropped)
				continue
			}
			return err
		}
		if wrRun > 0 {
			log.Printf("usb write recovered after %d timeouts (%d bytes dropped)", wrRun, wrDropped)
			wrRun, wrDropped = 0, 0
		}
		wedgedSince.Store(0)
		unanswered.CompareAndSwap(0, time.Now().UnixNano())
	}
}

var probeProfile string

// unanswered = unix-nano of the first packet sent since we last heard from the phone (0 = none pending).
var unanswered atomic.Int64

// wedgedSince = unix-nano of the first USB write timeout in the current run (0 = writes succeeding).
var wedgedSince atomic.Int64
var reuseAcc bool

func acquire(serial string, wait time.Duration) (*usbdev.Device, error) {
	all, err := usbdev.List()
	if err != nil {
		return nil, err
	}
	var src *usbdev.Info
	for i, d := range all {
		if usbdev.IsAccessory(d) {
			if reuseAcc {
				return usbdev.Open(d, true)
			}
			return nil, errors.New("phone is already in accessory mode (probably Tetrd's identity): unplug/replug the phone, then rerun")
		}
		if d.VID == samsungVID && (serial == "" || d.Serial == serial) && src == nil {
			src = &all[i]
		}
	}
	if src == nil {
		return nil, errors.New("no Samsung phone in normal mode found")
	}
	log.Printf("AOA source %04x:%04x serial=%q", src.VID, src.PID, src.Serial)
	d, err := usbdev.Open(*src, false)
	if err != nil {
		return nil, err
	}
	prot, err := d.GetProtocol()
	if err != nil || prot < 1 {
		d.Close()
		return nil, fmt.Errorf("AOA unsupported (proto=%d err=%v)", prot, err)
	}
	strs := []struct {
		i uint16
		s string
	}{{5, "mx-host-1"}, {0, "mxlab"}, {1, "usbtether"}, {2, "mx usb tether"}, {3, "1"}, {4, "https://localhost/"}}
	if probeProfile == "tetrd" {
		strs[0].s, strs[1].s, strs[2].s, strs[3].s, strs[4].s, strs[5].s = "MTkyNjU5MDI3MQpTQU1TVU5HX0FuZHJvaWQKMTc5Mjk2NjM4NDUwMQ", "robskie", "tether", "Tetrd USB Accessory", "1.0", "https://www.tetrd.app"
	}
	for _, s := range strs {
		if err := d.SendString(s.i, s.s); err != nil {
			d.Close()
			return nil, fmt.Errorf("SEND_STRING %d: %w", s.i, err)
		}
	}
	if err := d.StartAccessory(); err != nil {
		d.Close()
		return nil, fmt.Errorf("START: %w", err)
	}
	d.Close()
	log.Printf("AOA START sent (usb0 will drop); waiting for accessory")
	acc, err := usbdev.WaitAccessory(wait)
	if err != nil {
		return nil, err
	}
	return usbdev.Open(acc, true)
}

// dropPrivileges switches to the invoking user (SUDO_UID/SUDO_GID, else nobody)
// once every privileged resource (usbfs fd, TUN fd, addresses, routes) exists.
func dropPrivileges() error {
	uid, gid := 65534, 65534
	if v, err := strconv.Atoi(os.Getenv("SUDO_UID")); err == nil && v > 0 {
		uid = v
	}
	if v, err := strconv.Atoi(os.Getenv("SUDO_GID")); err == nil && v > 0 {
		gid = v
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 38 /*PR_SET_NO_NEW_PRIVS*/, 1, 0, 0, 0, 0); e != 0 {
		return e
	}
	if syscall.Setuid(0) == nil {
		return errors.New("able to regain root after drop")
	}
	log.Printf("dropped privileges to uid=%d gid=%d (no_new_privs)", uid, gid)
	return nil
}
