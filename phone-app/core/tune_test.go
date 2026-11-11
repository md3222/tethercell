package core

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func TestTuneTCPAccepted(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	st := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	defer st.Close()
	tuneTCP(st)
	if strings.Contains(buf.String(), "tcp option") {
		t.Fatal(buf.String())
	}
}

func TestFrameLenBig(t *testing.T) {
	p := make([]byte, 16000)
	p[0] = 0x60
	p[4], p[5] = byte((16000-40)>>8), byte((16000-40)&0xff)
	if frameLen(p) != 16000 {
		t.Fatal("16000-byte IPv6 packet rejected")
	}
}
