package core

import (
	"net"
	"time"

	"golang.org/x/net/icmp"
	xipv4 "golang.org/x/net/ipv4"
)

func csum(b []byte) uint16 {
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

// isEchoV4 reports whether p is an IPv4 ICMP echo request.
func isEchoV4(p []byte) bool {
	if len(p) < 28 || p[0]>>4 != 4 || p[9] != 1 {
		return false
	}
	ihl := int(p[0]&0xf) * 4
	return len(p) >= ihl+8 && p[ihl] == 8
}

// pingV4 forwards one echo request with an unprivileged ICMP datagram socket
// (Android allows these) and writes a hand-built echo reply back to the link.
func (t *tunnel) pingV4(p []byte) {
	ihl := int(p[0]&0xf) * 4
	src := net.IP(append([]byte(nil), p[12:16]...))
	dst := net.IP(append([]byte(nil), p[16:20]...))
	ic := p[ihl:]
	id, seq := ic[4:6], ic[6:8]
	data := ic[8:]

	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return
	}
	defer c.Close()
	m := icmp.Message{Type: xipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: int(id[0])<<8 | int(id[1]), Seq: int(seq[0])<<8 | int(seq[1]), Data: data}}
	b, err := m.Marshal(nil)
	if err != nil {
		return
	}
	if _, err := c.WriteTo(b, &net.UDPAddr{IP: dst}); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	for {
		n, peer, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		if pa, ok := peer.(*net.UDPAddr); !ok || !pa.IP.Equal(dst) {
			continue
		}
		rm, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || rm.Type != xipv4.ICMPTypeEchoReply {
			continue
		}
		re, ok := rm.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		out := make([]byte, 20+8+len(re.Data))
		out[0] = 0x45
		out[2], out[3] = byte(len(out)>>8), byte(len(out))
		out[6] = 0x40 // DF
		out[8] = 64
		out[9] = 1
		copy(out[12:16], dst.To4())
		copy(out[16:20], src.To4())
		cs := csum(out[:20])
		out[10], out[11] = byte(cs>>8), byte(cs)
		out[20] = 0 // echo reply
		copy(out[24:26], id)
		copy(out[26:28], seq)
		copy(out[28:], re.Data)
		cs = csum(out[20:])
		out[22], out[23] = byte(cs>>8), byte(cs)
		_ = t.write(out)
		return
	}
}
