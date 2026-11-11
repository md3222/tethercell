package host

import (
	"testing"

	"tetrd-clone/internal/frame"
)

func TestProbeIsValidEcho(t *testing.T) {
	p := probePacket(7)
	if frame.Len(p) != len(p) {
		t.Fatalf("probe fails frame validation: Len=%d want %d", frame.Len(p), len(p))
	}
	if p[9] != 1 || p[20] != 8 || cksum(p[20:]) != 0 {
		t.Fatalf("not a valid ICMP echo request: % x", p)
	}
}
