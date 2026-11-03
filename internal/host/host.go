// Command usbtether-host is the desktop half of Matt's own USB tether: it
// switches the phone into Android Open Accessory mode with OUR identity
// (mxlab/usbtether, not Tetrd's), creates a TUN, and moves bare IP datagrams
// over USB bulk. Not Tetrd-protocol-compatible by design.
//
// Safety: no routes are installed unless asked. Default is bring-up only;
// use -hostroute <ip>/32 for a one-destination test, -route for a default route.
package host

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tetrd-clone/internal/frame"
	"tetrd-clone/internal/tun"
	"tetrd-clone/internal/usbdev"
)

const (
	samsungVID = 0x04e8
	hostAddr   = "10.77.0.1/24"
	mtu        = 1400
)

// Main runs the host with the given flags (os.Args[1:] style) and exits the process:
// 0 = stopped, 3 = tunnel died after working, 4 = never delivered, 1 = start failed.
func Main(args []string) {
	fs := flag.NewFlagSet("usbtether run", flag.ExitOnError)
	iface := fs.String("iface", "mxtun0", "TUN interface name (never tetrd0)")
	hostRoute := fs.String("hostroute", "", "install ONE route, e.g. 1.1.1.1/32, via the TUN")
	route := fs.Bool("route", false, "install a default route via the TUN (metric 50: wins over wlan0)")
	serial := fs.String("serial", "", "USB serial of the phone in normal mode")
	drop := fs.Bool("drop", true, "drop root to SUDO_UID after the TUN/USB fds are open and routes installed")
	v6 := fs.Bool("v6", false, "also give the TUN a ULA IPv6 address (and a v6 default route with -route)")
	deadAfter := fs.Duration("dead-after", 10*time.Second, "declare the tunnel dead if data was sent and no valid packet came back for this long")
	wedgeAfter := fs.Duration("wedge-after", 6*time.Second, "declare the tunnel dead if every USB write has timed out for this long")
	clearHalt := fs.Bool("clear-halt", true, "CLEAR_HALT both bulk endpoints on attach (resets data toggles)")
	reuse := fs.Bool("reuse", false, "attach to a phone already in accessory mode (our identity) instead of requiring a replug")
	probe := fs.String("probe-profile", "", "DIAGNOSTIC: 'tetrd' sends the stock app identity to test whether the phone switches at all")
	wait := fs.Duration("wait", 20*time.Second, "wait for the accessory to enumerate")
	fs.Parse(args)
	log.SetPrefix("usbtether-host: ")
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if os.Geteuid() != 0 && !HasNetAdmin() {
		log.Fatal("needs CAP_NET_ADMIN (run the installed /usr/local/bin/usbtether, see 'usbtether setup') or root")
	}
	probeProfile = *probe
	reuseAcc = *reuse
	dev, err := acquire(*serial, *wait)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { dev.Release(0); dev.Close() }()
	log.Printf("accessory %04x:%04x in=%#x out=%#x", dev.Info.VID, dev.Info.PID, dev.In, dev.Out)
	// Reset both bulk endpoints (halt + data toggles) so a -reuse attach starts from a clean state.
	for _, ep := range []uint8{dev.In, dev.Out} {
		if !*clearHalt {
			break
		}
		if err := dev.ClearHalt(ep); err != nil {
			log.Printf("clear halt %#x: %v (continuing)", ep, err)
		}
	}

	td, err := tun.Open(*iface)
	if err != nil {
		log.Fatal(err)
	}
	defer td.Close()
	// Keep NetworkManager's hands off (it stripped nothing proven, but never let it guess).
	// If an NM profile "usbtether" (type tun) exists, hand the interface to NM *before* we add routes:
	// NM then owns addresses + DNS (so DNS survives Wi-Fi going away) and reports "connected" (browsers
	// stay online). Activation re-applies the device and wipes foreign routes, hence routes come last.
	if out, err := sysCmd("nmcli", "-g", "connection.id", "con", "show", "usbtether").Output(); err == nil && len(out) > 0 {
		_ = sysCmd("nmcli", "dev", "set", td.Name, "managed", "yes").Run()
		if o, err := sysCmd("nmcli", "con", "up", "usbtether", "ifname", td.Name).CombinedOutput(); err != nil {
			log.Printf("nmcli con up usbtether: %v: %s (continuing without NM)", err, o)
		} else {
			log.Printf("NetworkManager adopted %s (profile usbtether: DNS via tunnel)", td.Name)
		}
	} else {
		_ = sysCmd("nmcli", "dev", "set", td.Name, "managed", "no").Run()
	}

	must := func(args ...string) {
		if out, err := sysCmd("ip", args...).CombinedOutput(); err != nil {
			log.Fatalf("ip %v: %v: %s", args, err, out)
		}
	}
	must("link", "set", "dev", td.Name, "mtu", fmt.Sprint(mtu))
	must("addr", "replace", hostAddr, "dev", td.Name)
	must("link", "set", "dev", td.Name, "up")
	if *v6 {
		must("-6", "addr", "replace", "fd77:7777::1/64", "dev", td.Name, "nodad")
	}
	out, _ := sysCmd("ip", "-4", "-o", "addr", "show", "dev", td.Name).Output()
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
	go func() { // stats: with the phone's "core stats" line this names the hop that broke
		var last [5]int64
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Second):
			}
			cur := [5]int64{stUSBBytesIn.Load(), stPktsToTun.Load(), stBadFrames.Load(), stPktsToUSB.Load(), stWriteTimeouts.Load()}
			log.Printf("stats 10s: usbIn=%dB toTun=%d bad=%d toUSB=%d wTimeout=%d",
				cur[0]-last[0], cur[1]-last[1], cur[2]-last[2], cur[3]-last[3], cur[4]-last[4])
			last = cur
		}
	}()
	go func() { errc <- tunToUSB(dev, td, stop) }()

	go func() { // watchdog: data out but nothing back for too long = dead tunnel, give routes back
		var lastProbe time.Time
		var probeSeq uint16
		for {
			select {
			case <-stop:
				return
			case <-time.After(1 * time.Second):
			}
			if t := unanswered.Load(); t != 0 {
				pending := time.Since(time.Unix(0, t))
				if pending > *deadAfter {
					errc <- fmt.Errorf("no valid packet from phone for %s (probes unanswered)", *deadAfter)
					return
				}
				// A pure ACK never gets a reply, so silence alone proves nothing: after 3 s, ask the
				// phone directly. Its pingV4 answers this echo; the reply counts as a valid packet and
				// the kernel drops the unsolicited echo reply.
				if pending > 3*time.Second && time.Since(lastProbe) >= 2*time.Second {
					lastProbe = time.Now()
					probeSeq++
					select {
					case probeCh <- probePacket(probeSeq):
					default:
					}
				}
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
	if abnormal {
		dev.Release(0)
		dev.Close()
		if !everValid.Load() {
			os.Exit(4) // wrapper: 4 = never delivered a packet this session (wrong accessory / wedged phone)
		}
		os.Exit(3) // wrapper: 3 = tunnel died after working, reconnect; 0 = user stopped it
	}
}

// failover runs after an unexpected tunnel death: if Wi-Fi was switched off for the tunnel, bring it back.
func failover() {
	out, _ := sysCmd("nmcli", "-t", "-f", "DEVICE,STATE", "dev").Output()
	if !strings.Contains(string(out), "wlan0:connected") {
		log.Printf("failover: tunnel died, reconnecting wlan0")
		_ = sysCmd("nmcli", "radio", "wifi", "on").Run()
		_ = sysCmd("nmcli", "dev", "connect", "wlan0").Run()
	}
}

func usbToTun(d *usbdev.Device, td *tun.Device, stop <-chan struct{}) error {
	rd := make([]byte, 16384) // >= one phone transfer (the phone writes one packet per transfer)
	dumped := 0
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
		stUSBBytesIn.Add(int64(n))
		// Each transfer stands alone: never carry bytes over, so garbage cannot wedge the stream.
		good, bad := frame.Split(rd[:n], func(pkt []byte) {
			if _, err := td.Write(pkt); err != nil {
				log.Printf("tun write: %v", err)
				return
			}
			stPktsToTun.Add(1)
		})
		if good > 0 {
			everValid.Store(true)
			unanswered.Store(0) // only real packets count as "the phone is alive"
		}
		if bad {
			stBadFrames.Add(1)
			if dumped < 5 {
				dumped++
				m := n
				if m > 32 {
					m = 32
				}
				log.Printf("bad frame (%d bytes, %d good before it): % x", n, good, rd[:m])
			}
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
			case first = <-probeCh:
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
				stWriteTimeouts.Add(1)
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
		stPktsToUSB.Add(1)
		unanswered.CompareAndSwap(0, time.Now().UnixNano())
	}
}

var probeProfile string

// unanswered = unix-nano of the first packet sent since we last heard from the phone (0 = none pending).
var unanswered atomic.Int64

// wedgedSince = unix-nano of the first USB write timeout in the current run (0 = writes succeeding).
var wedgedSince atomic.Int64

// everValid = at least one valid packet reached the TUN this session (exit 3 vs 4).
var everValid atomic.Bool

// probeCh carries watchdog echo probes into the OUT path.
var probeCh = make(chan []byte, 1)

// probePacket builds an ICMPv4 echo request 10.77.0.1 -> 1.1.1.1 (id 0x6d78 "mx").
func probePacket(seq uint16) []byte {
	p := make([]byte, 28+8)
	p[0] = 0x45
	p[2], p[3] = byte(len(p)>>8), byte(len(p))
	p[8], p[9] = 64, 1
	copy(p[12:16], []byte{10, 77, 0, 1})
	copy(p[16:20], []byte{1, 1, 1, 1})
	c := cksum(p[:20])
	p[10], p[11] = byte(c>>8), byte(c)
	p[20] = 8 // echo request
	p[24], p[25] = 0x6d, 0x78
	p[26], p[27] = byte(seq>>8), byte(seq)
	copy(p[28:], "mxprobe!")
	c = cksum(p[20:])
	p[22], p[23] = byte(c>>8), byte(c)
	return p
}

func cksum(b []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// counters for the 10 s stats line
var stUSBBytesIn, stPktsToTun, stBadFrames, stPktsToUSB, stWriteTimeouts atomic.Int64
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
				return openRetry(d, true)
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
	d, err := openRetry(*src, false)
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
	return openRetry(acc, true)
}

// dropPrivileges switches to the invoking user (SUDO_UID/SUDO_GID, else nobody)
// once every privileged resource (usbfs fd, TUN fd, addresses, routes) exists.
func dropPrivileges() error {
	if os.Geteuid() != 0 {
		if err := DropAllCaps(); err != nil {
			return err
		}
		log.Printf("dropped all capabilities (uid=%d, no_new_privs)", os.Getuid())
		return nil
	}
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
