// Command tetrd-clone is an original, independent desktop daemon that
// interoperates with the existing Tetrd 1.3.2 phone app over the decoded
// AOA + RPC protocol. It replaces the desktop half only: it puts the phone
// into accessory mode, speaks the control ladder, creates a host TUN, and
// moves bare IP datagrams over USB bulk.
//
// See ../REA-2026-10-31.md for the protocol this implements.
package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tetrd-clone/internal/proto"
	"tetrd-clone/internal/tun"
	"tetrd-clone/internal/usbdev"
)

const (
	aoaVendor  = 0x04e8 // Samsung
	deviceID   = 1926590271
	deviceName = "SAMSUNG_Android"
)

func main() {
	iface := flag.String("iface", "tetrd0", "TUN interface name")
	aoa := flag.Bool("aoa", true, "put the phone into accessory mode if not already")
	route := flag.Bool("route", false, "install default routes through the TUN (phone becomes exit node)")
	serial := flag.String("serial", "", "USB serial of the AOA source device (empty = first Samsung)")
	waitT := flag.Duration("wait", 15*time.Second, "how long to wait for the phone to re-enumerate")
	flag.Parse()

	log.SetPrefix("tetrd-clone: ")
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if os.Geteuid() != 0 {
		log.Fatal("must run as root: usbfs bulk ioctls and TUNSETIFF both need it")
	}

	dev, err := acquireAccessory(*aoa, *serial, *waitT)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		dev.Release(0)
		dev.Close()
	}()
	log.Printf("accessory %04x:%04x serial=%q in=%#x out=%#x",
		dev.Info.VID, dev.Info.PID, dev.Info.Serial, dev.In, dev.Out)

	td, err := tun.Open(*iface)
	if err != nil {
		log.Fatal(err)
	}
	defer td.Close()
	log.Printf("tun %q opened", td.Name)

	if err := td.Configure("10.0.3.1/24", "2001:feed:beef::1/64", 1500, *route); err != nil {
		log.Fatal(err)
	}
	log.Printf("tun %q up (route=%v)", td.Name, *route)

	peer := proto.New(dev, func(f string, a ...any) { log.Printf(f, a...) })
	ladder := proto.NewLadder(peer, func(f string, a ...any) { log.Printf(f, a...) })
	ladder.Install()

	peer.OnIP(func(pkt []byte) {
		if _, err := td.Write(pkt); err != nil {
			log.Printf("tun write: %v", err)
		}
	})

	stop := make(chan struct{})
	done := make(chan error, 2)

	go func() { done <- peer.Run() }()
	go func() { done <- pumpTUN(td, peer) }()

	if err := ladder.Start(); err != nil {
		log.Fatalf("handshake: %v", err)
	}
	log.Printf("RPC Version + SetLogLevelV2 ok; waiting for the phone to connect")

	go ladder.KeepAlive(stop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case s := <-sig:
		log.Printf("signal %v, shutting down", s)
	case err := <-done:
		if err != nil {
			log.Printf("fatal: %v", err)
		} else {
			log.Printf("peer closed")
		}
	}
	close(stop)
}

// pumpTUN forwards host IP datagrams to the phone over bulk OUT.
func pumpTUN(td *tun.Device, p *proto.Peer) error {
	buf := make([]byte, 65536)
	for {
		n, err := td.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := p.WriteUSB(buf[:n]); err != nil {
			return err
		}
	}
}

type aoaString struct {
	index uint16
	value string
}

// aoaStrings builds the AOA identity strings in the order the daemon sends
// them: serial (index 5) first, then 0..4. The serial is unpadded base64 of
// "<deviceID>\n<name>\n<unix_ms>".
func aoaStrings() []aoaString {
	ts := time.Now().UnixMilli()
	s := fmt.Sprintf("%d\n%s\n%d", deviceID, deviceName, ts)
	b64 := base64.RawStdEncoding.EncodeToString([]byte(s))
	return []aoaString{
		{5, b64},
		{0, "robskie"},
		{1, "tether"},
		{2, "Tetrd USB Accessory"},
		{3, "1.0"},
		{4, "https://www.tetrd.app"},
	}
}

func acquireAccessory(doAOA bool, serial string, wait time.Duration) (*usbdev.Device, error) {
	all, err := usbdev.List()
	if err == nil {
		for _, d := range all {
			if usbdev.IsAccessory(d) {
				return usbdev.Open(d, true)
			}
		}
	}
	if !doAOA {
		return nil, errors.New("no accessory device present and -aoa=false")
	}
	src, ok := findSource(serial)
	if !ok {
		return nil, errors.New("no Samsung phone in normal mode found (plug it in, or stop Tetrd and replug)")
	}
	log.Printf("AOA source %04x:%04x product=%q serial=%q at %s",
		src.VID, src.PID, src.Product, src.Serial, src.Path())

	d, err := usbdev.Open(src, false)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer d.Close()

	prot, err := d.GetProtocol()
	if err != nil {
		return nil, fmt.Errorf("AOA GET_PROTOCOL: %w", err)
	}
	log.Printf("AOA protocol version %d", prot)
	if prot < 1 {
		return nil, errors.New("device reports AOA protocol 0 (no accessory support)")
	}
	for _, s := range aoaStrings() {
		if err := d.SendString(s.index, s.value); err != nil {
			return nil, fmt.Errorf("AOA SEND_STRING %d: %w", s.index, err)
		}
		log.Printf("AOA string %d (%d bytes)", s.index, len(s.value))
	}
	if err := d.StartAccessory(); err != nil {
		return nil, fmt.Errorf("AOA START: %w", err)
	}
	log.Printf("AOA START sent; waiting for re-enumeration")

	acc, err := usbdev.WaitAccessory(wait)
	if err != nil {
		return nil, err
	}
	return usbdev.Open(acc, true)
}

func findSource(serial string) (usbdev.Info, bool) {
	all, err := usbdev.List()
	if err != nil {
		return usbdev.Info{}, false
	}
	var first usbdev.Info
	found := false
	for _, d := range all {
		if d.VID != aoaVendor {
			continue
		}
		if !found {
			first = d
			found = true
		}
		if serial != "" && d.Serial == serial {
			return d, true
		}
	}
	if found && serial == "" {
		return first, true
	}
	return usbdev.Info{}, false
}
