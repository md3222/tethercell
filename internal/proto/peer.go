package proto

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"tetrd-clone/internal/usbdev"
	"tetrd-clone/internal/wire"
)

// Handler is called for an inbound call whose header and args have both
// arrived. It runs on the reader goroutine: it must not block waiting for a
// reply from the peer (spawn a goroutine for multi-step work).
type Handler func(seq int64, argsBody []byte)

type result struct {
	vals []wire.Value
	tail []byte
}

// Peer is one end of the RPC-over-bulk channel.
type Peer struct {
	dev    *usbdev.Device
	writeM sync.Mutex

	mu       sync.Mutex
	nextSeq  int64
	pending  map[int64]chan result
	hdrs     map[int64]string // inbound call headers waiting for args
	handlers map[string]Handler

	ip  func([]byte)
	log func(string, ...any)
}

// New returns a peer bound to an open accessory device.
func New(dev *usbdev.Device, log func(string, ...any)) *Peer {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Peer{
		dev:      dev,
		pending:  make(map[int64]chan result),
		hdrs:     make(map[int64]string),
		handlers: make(map[string]Handler),
		log:      log,
	}
}

// Handle registers a handler for an inbound method.
func (p *Peer) Handle(method string, h Handler) {
	p.mu.Lock()
	p.handlers[method] = h
	p.mu.Unlock()
}

// OnIP registers the sink for inbound IP datagrams.
func (p *Peer) OnIP(f func([]byte)) { p.ip = f }

// WriteUSB writes one or more already-encoded frames as a single bulk write.
func (p *Peer) WriteUSB(b []byte) error {
	p.writeM.Lock()
	defer p.writeM.Unlock()
	for len(b) > 16384 {
		if _, err := p.dev.BulkWrite(b[:16384], 5*time.Second); err != nil {
			return err
		}
		b = b[16384:]
	}
	_, err := p.dev.BulkWrite(b, 5*time.Second)
	return err
}

func (p *Peer) allocSeq() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextSeq += 2
	return p.nextSeq
}

// beginCall registers a pending reply slot and returns the sequence number and
// channel, plus a done func that unregisters it.
func (p *Peer) beginCall() (int64, chan result, func()) {
	p.mu.Lock()
	p.nextSeq += 2
	seq := p.nextSeq
	ch := make(chan result, 1)
	p.pending[seq] = ch
	p.mu.Unlock()
	return seq, ch, func() {
		p.mu.Lock()
		delete(p.pending, seq)
		p.mu.Unlock()
	}
}

func await(ch chan result, timeout time.Duration, method string) ([]wire.Value, []byte, error) {
	select {
	case r := <-ch:
		return r.vals, r.tail, nil
	case <-time.After(timeout):
		return nil, nil, fmt.Errorf("proto: %s: timeout", method)
	}
}

// Call sends a call whose args body is built for the allocated sequence
// number, and waits for the peer's value frame.
func (p *Peer) Call(method string, args func(seq int64) []byte, timeout time.Duration) ([]wire.Value, []byte, error) {
	seq, ch, done := p.beginCall()
	defer done()
	out := append(wire.EncodeFrame(wire.TypeCall, wire.CallHead(seq, method)),
		wire.EncodeFrame(wire.TypeCall, args(seq))...)
	if err := p.WriteUSB(out); err != nil {
		return nil, nil, err
	}
	return await(ch, timeout, method)
}

// CallRaw sends a call whose args frame is a captured fixture. The fixture's
// sequence number is rewritten to the allocated one.
func (p *Peer) CallRaw(method string, argsFrame []byte, timeout time.Duration) ([]wire.Value, []byte, error) {
	seq, ch, done := p.beginCall()
	defer done()
	af, err := wire.SetSeq(argsFrame, seq)
	if err != nil {
		return nil, nil, fmt.Errorf("proto: %s: %w", method, err)
	}
	out := append(wire.EncodeFrame(wire.TypeCall, wire.CallHead(seq, method)), af...)
	if err := p.WriteUSB(out); err != nil {
		return nil, nil, err
	}
	return await(ch, timeout, method)
}

// SendReply sends a successful reply header plus an empty value frame.
func (p *Peer) SendReply(seq int64, method string) error {
	out := append(wire.EncodeFrame(wire.TypeReply, wire.ReplyHead(seq, method)),
		wire.EncodeFrame(wire.TypeReply, wire.ReplyValue(seq))...)
	return p.WriteUSB(out)
}

// SendReplyValue sends a reply header plus a caller-supplied value body.
func (p *Peer) SendReplyValue(seq int64, method string, valueBody []byte) error {
	out := append(wire.EncodeFrame(wire.TypeReply, wire.ReplyHead(seq, method)),
		wire.EncodeFrame(wire.TypeReply, valueBody)...)
	return p.WriteUSB(out)
}

// SendReplyRaw sends a reply header plus a captured value frame. The value
// frame's sequence number is rewritten to seq.
func (p *Peer) SendReplyRaw(seq int64, method string, valueFrame []byte) error {
	vf, err := wire.SetSeq(valueFrame, seq)
	if err != nil {
		return err
	}
	out := append(wire.EncodeFrame(wire.TypeReply, wire.ReplyHead(seq, method)), vf...)
	return p.WriteUSB(out)
}

// Run reads the bulk endpoint until it fails. It dispatches RPC frames and
// hands IP datagrams to the OnIP sink.
func (p *Peer) Run() error {
	buf := make([]byte, 128*1024)
	var acc []byte
	for {
		n, err := p.dev.BulkRead(buf, 30*time.Second)
		if err != nil {
			// usbfs reports an idle timeout as ETIMEDOUT; only a dead
			// device ends the loop.
			if isFatalUSB(err) {
				return err
			}
			continue
		}
		if n > 0 {
			acc = append(acc, buf[:n]...)
		}
		for len(acc) > 0 {
			used, need := p.step(acc)
			if need {
				break
			}
			acc = acc[used:]
		}
		if len(acc) > 8*1024*1024 {
			return fmt.Errorf("proto: receive buffer overflow")
		}
	}
}

// step consumes one frame or datagram from b. need means b holds only a
// prefix and the caller should read more.
func (p *Peer) step(b []byte) (used int, need bool) {
	switch b[0] {
	case wire.TypeCall, wire.TypeReply:
		fr, rest, err := wire.Split(b)
		if err != nil {
			if errors.Is(err, wire.ErrShort) {
				return 0, true
			}
			// Desync: drop one byte and rescan.
			return 1, false
		}
		p.dispatch(fr)
		return len(b) - len(rest), false
	default:
		if n, ok := wire.IPLength(b); ok {
			if p.ip != nil {
				p.ip(b[:n])
			}
			return n, false
		}
		return 1, false
	}
}

func isFatalUSB(err error) bool {
	for _, target := range []error{syscall.ENODEV, syscall.EPIPE, syscall.ESHUTDOWN, syscall.ENOENT} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (p *Peer) dispatch(fr wire.Frame) {
	vals, tail, err := wire.ParseValues(fr.Body)
	if err != nil || len(vals) == 0 || vals[0].Tag != wire.TagInt {
		p.log("bad frame: %v %q", err, fr.Body)
		return
	}
	kind := vals[0].Int
	if len(vals) < 2 || vals[1].Tag != wire.TagInt {
		p.log("frame without seq (kind=%d)", kind)
		return
	}
	seq := vals[1].Int
	switch kind {
	case 0: // call head
		if len(vals) < 3 || vals[2].Tag != wire.TagString {
			p.log("call head without method")
			return
		}
		p.mu.Lock()
		p.hdrs[seq] = vals[2].Str
		p.mu.Unlock()
	case 2: // call args
		p.mu.Lock()
		method, ok := p.hdrs[seq]
		delete(p.hdrs, seq)
		h := p.handlers[method]
		p.mu.Unlock()
		if !ok {
			p.log("args for unknown seq %d", seq)
			return
		}
		if h == nil {
			p.log("no handler for %q seq=%d", method, seq)
			_ = p.SendReply(seq, method)
			return
		}
		h(seq, fr.Body)
	case 4: // reply head: the value frame follows
		_ = seq
	case 6: // reply value
		p.mu.Lock()
		ch := p.pending[seq]
		p.mu.Unlock()
		if ch == nil {
			p.log("value frame for unknown seq %d", seq)
			return
		}
		select {
		case ch <- result{vals: vals, tail: tail}:
		default:
		}
	default:
		p.log("unknown frame kind %d", kind)
	}
}
