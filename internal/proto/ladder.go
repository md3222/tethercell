package proto

import (
	_ "embed"
	"time"

	"tetrd-clone/internal/wire"
)

// Captured frames replayed verbatim (sequence numbers are rewritten per call).
// See ../REA-2026-10-31.md §3.2 for the ladder they came from.
//
//go:embed fixtures/configv2_reply.bin
var configV2Reply []byte

//go:embed fixtures/netconfigv2_args.bin
var netConfigV2Args []byte

//go:embed fixtures/ondeviceconnect_args.bin
var onDeviceConnectArgs []byte

//go:embed fixtures/ondeviceconnected_args.bin
var onDeviceConnectedArgs []byte

//go:embed fixtures/ondeviceauthorize_args.bin
var onDeviceAuthorizeArgs []byte

//go:embed fixtures/ondeviceauthorized_args.bin
var onDeviceAuthorizedArgs []byte

const (
	protoVersion = 4 // returned to the phone's Version call, matches capture
	callTimeout  = 5 * time.Second
)

// Ladder drives the desktop side of the Tetrd control plane: it installs
// handlers for the calls the phone makes, and provides the connect/authorize
// sequences the desktop must issue in response.
type Ladder struct {
	p   *Peer
	log func(string, ...any)
}

// NewLadder returns a ladder bound to a peer.
func NewLadder(p *Peer, log func(string, ...any)) *Ladder {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Ladder{p: p, log: log}
}

// Install registers the inbound-call handlers. Call before Start.
func (l *Ladder) Install() {
	l.p.Handle("Version", func(seq int64, _ []byte) {
		if err := l.p.SendReplyValue(seq, "Version", wire.ReplyInt(seq, "int", protoVersion)); err != nil {
			l.log("reply Version: %v", err)
		}
	})
	l.p.Handle("ConfigV2", func(seq int64, _ []byte) {
		if err := l.p.SendReplyRaw(seq, "ConfigV2", configV2Reply); err != nil {
			l.log("reply ConfigV2: %v", err)
		}
	})
	l.p.Handle("ConnectDeviceV2", func(seq int64, _ []byte) {
		go l.connectDevice(seq)
	})
	l.p.Handle("AuthorizeDeviceV2", func(seq int64, _ []byte) {
		go l.authorize(seq)
	})
	l.p.Handle("CurrentTimeV2", func(seq int64, _ []byte) {
		if err := l.p.SendReplyValue(seq, "CurrentTimeV2", wire.ReplyInt(seq, "int64", time.Now().UnixMilli())); err != nil {
			l.log("reply CurrentTimeV2: %v", err)
		}
	})
	l.p.Handle("KeepDeviceAliveV2", func(seq int64, _ []byte) {
		if err := l.p.SendReply(seq, "KeepDeviceAliveV2"); err != nil {
			l.log("reply KeepDeviceAliveV2: %v", err)
		}
	})
}

// Start performs the desktop-initiated half of the handshake.
func (l *Ladder) Start() error {
	if _, _, err := l.p.Call("Version", wire.Args, callTimeout); err != nil {
		return err
	}
	if _, _, err := l.p.Call("SetLogLevelV2", func(seq int64) []byte {
		return wire.ArgsInt(seq, "log.Level", 0)
	}, callTimeout); err != nil {
		return err
	}
	return nil
}

// connectDevice handles the phone's ConnectDeviceV2: it pushes the desktop's
// device info, asks the phone for its config, offers the network plan, and
// finally acknowledges the connect.
func (l *Ladder) connectDevice(seq int64) {
	l.log("ConnectDeviceV2 seq=%d", seq)
	if _, _, err := l.p.CallRaw("OnDeviceConnectV2", onDeviceConnectArgs, callTimeout); err != nil {
		l.log("OnDeviceConnectV2: %v", err)
		return
	}
	if _, _, err := l.p.Call("ConfigV2", wire.Args, callTimeout); err != nil {
		l.log("ConfigV2: %v", err)
		return
	}
	if _, _, err := l.p.CallRaw("ConnectV2", netConfigV2Args, callTimeout); err != nil {
		l.log("ConnectV2: %v", err)
		return
	}
	if _, _, err := l.p.CallRaw("OnDeviceConnectedV2", onDeviceConnectedArgs, callTimeout); err != nil {
		l.log("OnDeviceConnectedV2: %v", err)
		return
	}
	if err := l.p.SendReply(seq, "ConnectDeviceV2"); err != nil {
		l.log("reply ConnectDeviceV2: %v", err)
		return
	}
	l.log("device connected")
}

// authorize handles the phone's AuthorizeDeviceV2, which it issues after the
// user approves the connection in the app.
func (l *Ladder) authorize(seq int64) {
	l.log("AuthorizeDeviceV2 seq=%d", seq)
	if _, _, err := l.p.CallRaw("OnDeviceAuthorizeV2", onDeviceAuthorizeArgs, callTimeout); err != nil {
		l.log("OnDeviceAuthorizeV2: %v", err)
		return
	}
	if err := l.p.SendReply(seq, "AuthorizeDeviceV2"); err != nil {
		l.log("reply AuthorizeDeviceV2: %v", err)
		return
	}
	if _, _, err := l.p.CallRaw("OnDeviceAuthorizedV2", onDeviceAuthorizedArgs, callTimeout); err != nil {
		l.log("OnDeviceAuthorizedV2: %v", err)
		return
	}
	l.log("device authorized")
}

// KeepAlive sends KeepAliveV2 every 300ms, matching the daemon's cadence.
// It returns when stop is closed or the peer dies.
func (l *Ladder) KeepAlive(stop <-chan struct{}) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Fire-and-forget so a slow reply cannot stall the ticker.
			go func() {
				if _, _, err := l.p.Call("KeepAliveV2", wire.Args, 2*time.Second); err != nil {
					l.log("KeepAliveV2: %v", err)
				}
			}()
		}
	}
}
