// frame.go (copy of clone/internal/frame) splits one USB bulk transfer into bare IP datagrams.
//
// Both ends write whole packets per transfer (phone: one packet per write;
// host: a batch of whole packets <=16383 bytes), and each reader's buffer is
// at least one transfer, so every transfer is parsed on its own: nothing is
// carried into the next read. A garbled or truncated transfer therefore costs
// that transfer only, instead of desyncing the stream until a restart.
package core

// maxPacket is the largest datagram accepted (link MTU 16000; a transfer is <=16384 bytes).
const maxPacket = 16384

// frameLen validates the datagram at the head of b and returns its length, or 0 if
// b does not start with a plausible, complete IPv4/IPv6 packet.
func frameLen(b []byte) int {
	if len(b) < 20 {
		return 0
	}
	switch b[0] >> 4 {
	case 4:
		ihl := int(b[0]&0xf) * 4
		total := int(b[2])<<8 | int(b[3])
		if ihl < 20 || total < ihl || total > maxPacket || total > len(b) {
			return 0
		}
		if hdrSum(b[:ihl]) != 0xffff {
			return 0
		}
		return total
	case 6:
		if len(b) < 40 {
			return 0
		}
		total := 40 + (int(b[4])<<8 | int(b[5]))
		if total > maxPacket || total > len(b) {
			return 0
		}
		return total
	}
	return 0
}

// frameSplit calls fn for every valid datagram in one transfer, skipping 0x00
// padding. It stops at the first invalid byte sequence and drops the rest.
// fn must not retain pkt. Returns the number of good packets and whether
// the transfer had to be (partly) dropped.
func frameSplit(b []byte, fn func(pkt []byte)) (good int, bad bool) {
	for len(b) > 0 {
		if b[0] == 0 {
			b = b[1:]
			continue
		}
		n := frameLen(b)
		if n == 0 {
			return good, true
		}
		fn(b[:n])
		good++
		b = b[n:]
	}
	return good, false
}

// hdrSum is the ones'-complement sum of a header (0xffff when the checksum is right).
func hdrSum(h []byte) uint16 {
	var s uint32
	for i := 0; i+1 < len(h); i += 2 {
		s += uint32(h[i])<<8 | uint32(h[i+1])
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return uint16(s)
}
