// Package core is the phone-side exit node: bare IP datagrams arrive on the
// USB accessory fd, a userspace netstack terminates each flow, and real
// Android sockets re-dial the destination. Original code; own wire format.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

var maxBatch = 0 // >0 coalesces writes; measured slower+erratic on this phone, keep 0

const (
	nicID   = 1
	mtu     = 1400
	maxRead = 65536
)

var (
	nReadErr, nRead, nInj, nOut, nTCP, nTCPerr, nUDP, nPing atomic.Int64
	mu                                                      sync.Mutex
	running                                                 *tunnel
)

type tunnel struct {
	f      *os.File
	st     *stack.Stack
	ep     *channel.Endpoint
	cancel context.CancelFunc
	done   chan struct{}
}

// Start takes ownership of fd (the detached accessory file descriptor).
func Start(fd int) error {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return errors.New("already running")
	}
	f := os.NewFile(uintptr(fd), "accessory")
	if f == nil {
		return errors.New("bad fd")
	}
	st := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6},
	})
	ep := channel.New(1024, mtu, "")
	if e := st.CreateNIC(nicID, ep); e != nil {
		f.Close()
		return fmt.Errorf("CreateNIC: %v", e)
	}
	st.SetPromiscuousMode(nicID, true)
	st.SetSpoofing(nicID, true)
	st.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	tf := tcp.NewForwarder(st, 0, 2048, func(r *tcp.ForwarderRequest) { handleTCP(r) })
	st.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)
	uf := udp.NewForwarder(st, func(r *udp.ForwarderRequest) bool { handleUDP(r); return true })
	st.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)

	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{f: f, st: st, ep: ep, cancel: cancel, done: make(chan struct{})}
	running = t
	go func() {
		for ctx.Err() == nil {
			time.Sleep(3 * time.Second)
			log.Printf("core stats: readErr=%d usbRead=%d inject=%d out=%d tcp=%d tcpErr=%d udp=%d ping=%d",
				nReadErr.Load(), nRead.Load(), nInj.Load(), nOut.Load(), nTCP.Load(), nTCPerr.Load(), nUDP.Load(), nPing.Load())
		}
	}()
	go t.usbToStack()
	go t.stackToUSB(ctx)
	return nil
}

// Run is Start that blocks until the tunnel stops (EOF/IO error/Stop).
func Run(fd int) error {
	if err := Start(fd); err != nil {
		return err
	}
	mu.Lock()
	t := running
	mu.Unlock()
	if t != nil {
		<-t.done
	}
	return nil
}

// Stop tears the tunnel down (closes the fd).
func Stop() {
	mu.Lock()
	t := running
	running = nil
	mu.Unlock()
	if t == nil {
		return
	}
	t.cancel()
	t.f.Close()
	t.st.Close()
	close(t.done)
}

// Running reports whether a tunnel is up.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return running != nil
}

// ipLen returns the total length of the datagram at the head of b, or 0 if
// more bytes are needed, or -1 if b does not start with an IP header.
func ipLen(b []byte) int {
	if len(b) < 1 {
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

func (t *tunnel) usbToStack() {
	buf := make([]byte, 0, 2*maxRead)
	tmp := make([]byte, 16384)
	errRun := 0
	for {
		n, err := t.f.Read(tmp)
		if n > 0 {
			nRead.Add(1)
			buf = append(buf, tmp[:n]...)
			for {
				for len(buf) > 0 && buf[0] == 0 { // 0x00 = padding
					buf = buf[1:]
				}
				l := ipLen(buf)
				if l < 0 {
					buf = buf[:0] // desync: drop and resync on next transfer
					break
				}
				if l == 0 || l > len(buf) {
					break
				}
				t.inject(buf[:l])
				buf = buf[l:]
			}
			if len(buf) == 0 {
				buf = buf[:0:cap(buf)]
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.ENODEV) {
				Stop()
				return
			}
			// Transient (e.g. EIO while the gadget re-queues): keep the netstack and retry.
			nReadErr.Add(1)
			log.Printf("usb read (retry %d): %v", errRun+1, err)
			errRun++
			if errRun > 200 {
				Stop()
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		errRun = 0
	}
}

func (t *tunnel) inject(p []byte) {
	nInj.Add(1)
	if isEchoV4(p) {
		nPing.Add(1)
		go t.pingV4(append([]byte(nil), p...))
		return
	}
	var proto tcpip.NetworkProtocolNumber
	switch p[0] >> 4 {
	case 4:
		proto = header.IPv4ProtocolNumber
	case 6:
		proto = header.IPv6ProtocolNumber
	default:
		return
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), p...))})
	t.ep.InjectInbound(proto, pkt)
	pkt.DecRef()
}

func (t *tunnel) stackToUSB(ctx context.Context) {
	out := make([]byte, 0, 16384)
	add := func(pkt *stack.PacketBuffer) {
		v := pkt.ToView()
		out = append(out, v.AsSlice()...)
		v.Release()
		pkt.DecRef()
	}
	for {
		pkt := t.ep.ReadContext(ctx)
		if pkt == nil {
			return
		}
		out = out[:0]
		add(pkt)
		for len(out) < maxBatch {
			p := t.ep.Read()
			if p == nil {
				break
			}
			add(p)
		}
		if len(out)%512 == 0 {
			out = append(out, 0) // keep the transfer off a max-packet multiple (no ZLP needed)
		}
		nOut.Add(1)
		if _, err := t.f.Write(out); err != nil {
			Stop()
			return
		}
	}
}

func handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := net.JoinHostPort(id.LocalAddress.String(), fmt.Sprint(id.LocalPort))
	nTCP.Add(1)
	out, err := net.DialTimeout("tcp", dst, 10*time.Second)
	if err != nil {
		nTCPerr.Add(1)
		log.Printf("tcp dial %s: %v", dst, err)
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		out.Close()
		r.Complete(true)
		return
	}
	r.Complete(false)
	in := gonet.NewTCPConn(&wq, ep)
	go pipe(in, out)
}

func handleUDP(r *udp.ForwarderRequest) {
	nUDP.Add(1)
	id := r.ID()
	var wq waiter.Queue
	// Create the endpoint synchronously: a resolver sends A+AAAA back to back from one
	// socket, and the second datagram must find this endpoint instead of racing its creation.
	ep, e := r.CreateEndpoint(&wq)
	if e != nil {
		return
	}
	in := gonet.NewUDPConn(&wq, ep)
	dst := net.JoinHostPort(id.LocalAddress.String(), fmt.Sprint(id.LocalPort))
	go func() {
		out, err := net.Dial("udp", dst)
		if err != nil {
			in.Close()
			return
		}
		pipeUDP(in, out)
	}()
}

func pipeUDP(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 65535)
		for {
			_ = src.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, err := src.Read(buf)
			if err != nil {
				a.Close()
				b.Close()
				return
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
	go cp(a, b)
	cp(b, a)
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_ = src.SetReadDeadline(time.Time{})
		io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}
