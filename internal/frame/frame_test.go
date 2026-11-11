package frame

import "testing"

func v4(payload int) []byte {
	p := make([]byte, 20+payload)
	p[0] = 0x45
	p[2], p[3] = byte(len(p)>>8), byte(len(p))
	p[8], p[9] = 64, 17
	copy(p[12:], []byte{10, 77, 0, 1, 1, 1, 1, 1})
	s := ^sum(p[:20])
	p[10], p[11] = byte(s>>8), byte(s)
	return p
}

func v6(payload int) []byte {
	p := make([]byte, 40+payload)
	p[0] = 0x60
	p[4], p[5] = byte(payload>>8), byte(payload)
	p[6], p[7] = 17, 64
	return p
}

func cat(bs ...[]byte) (o []byte) {
	for _, b := range bs {
		o = append(o, b...)
	}
	return
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		good int
		bad  bool
	}{
		{"one v4", v4(36), 1, false},
		{"v4+v6+pad", cat(v4(8), v6(100), []byte{0}), 2, false},
		{"leading pad", cat([]byte{0, 0}, v4(8)), 1, false},
		{"truncated tail", cat(v4(8), v4(100)[:50]), 1, true},
		{"garbage prefix", cat([]byte{0x47, 0x11, 0x22}, v4(8)), 0, true},
		{"bogus v4 length", func() []byte { p := v4(8); p[2], p[3] = 0xff, 0xf0; return p }(), 0, true},
		{"bad checksum", func() []byte { p := v4(8); p[10] ^= 0xff; return p }(), 0, true},
		{"oversize v6", v6(MaxPacket), 0, true},
		{"max v4", v4(MaxPacket - 20), 1, false},
		{"empty", nil, 0, false},
	}
	for _, c := range cases {
		good, bad := Split(c.in, func([]byte) {})
		if good != c.good || bad != c.bad {
			t.Errorf("%s: got good=%d bad=%v, want %d %v", c.name, good, bad, c.good, c.bad)
		}
	}
}
