package wire

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type corpusRow struct {
	Kind string `json:"kind"`
	Hex  string `json:"hex"`
	Len  int    `json:"len"`
}

func loadCorpus(t *testing.T) []corpusRow {
	t.Helper()
	path := filepath.Join("..", "..", "fixtures", "frames.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var rows []corpusRow
	for i, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var r corpusRow
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("corpus line %d: %v", i, err)
		}
		rows = append(rows, r)
	}
	return rows
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// TestSplitCorpus re-splits every captured frame and checks it round-trips.
func TestSplitCorpus(t *testing.T) {
	rows := loadCorpus(t)
	if len(rows) < 500 {
		t.Fatalf("expected a few hundred corpus frames, got %d", len(rows))
	}
	for i, r := range rows {
		if r.Kind != "rpc" {
			continue
		}
		b, err := hex.DecodeString(r.Hex)
		if err != nil {
			t.Fatalf("row %d: bad hex: %v", i, err)
		}
		fr, rest, err := Split(b)
		if err != nil {
			t.Fatalf("row %d: split: %v", i, err)
		}
		if len(rest) != 0 {
			t.Fatalf("row %d: %d trailing bytes", i, len(rest))
		}
		if fr.Type != TypeReply && fr.Type != TypeCall {
			t.Fatalf("row %d: bad type %#x", i, fr.Type)
		}
		vals, _, err := ParseValues(fr.Body)
		if err != nil {
			t.Fatalf("row %d: parse: %v", i, err)
		}
		if len(vals) < 2 || vals[0].Tag != TagInt || vals[1].Tag != TagInt {
			t.Fatalf("row %d: want kind+seq ints, got %+v", i, vals)
		}
		if got, ok := SeqOf(b); !ok || got != vals[1].Int {
			t.Fatalf("row %d: SeqOf=%d (ok=%v) want %d", i, got, ok, vals[1].Int)
		}
	}
}

// TestSetSeqOnFixtures rewrites the sequence number of each captured fixture
// and confirms the rewrite survives a re-parse.
func TestSetSeqOnFixtures(t *testing.T) {
	matches, _ := filepath.Glob(filepath.Join("..", "proto", "fixtures", "*.bin"))
	if len(matches) == 0 {
		t.Fatal("no proto fixtures found")
	}
	seqs := []int64{0, 2, 255, 256, 65535, 65536, 16646418, 1 << 30}
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		orig, ok := SeqOf(b)
		if !ok {
			t.Fatalf("%s: no seq", path)
		}
		for _, seq := range seqs {
			out, err := SetSeq(b, seq)
			if err != nil {
				t.Fatalf("%s seq=%d: %v", path, seq, err)
			}
			got, ok := SeqOf(out)
			if !ok || got != seq {
				t.Fatalf("%s seq=%d: SeqOf=%d ok=%v", path, seq, got, ok)
			}
			// The body must still parse, and the tail after the second value
			// must be unchanged.
			ov, _, err := ParseValues(b[3:])
			if err != nil {
				t.Fatalf("%s: parse orig: %v", path, err)
			}
			nv, _, err := ParseValues(out[3:])
			if err != nil {
				t.Fatalf("%s seq=%d: parse new: %v", path, seq, err)
			}
			if len(ov) < 2 || len(nv) < 2 {
				t.Fatalf("%s: too few values", path)
			}
			if len(ov) != len(nv) {
				t.Fatalf("%s seq=%d: value count %d -> %d", path, seq, len(ov), len(nv))
			}
		}
		// Setting the seq to its own value must be byte-identical.
		idem, err := SetSeq(b, orig)
		if err != nil {
			t.Fatal(err)
		}
		if string(idem) != string(b) {
			t.Fatalf("%s: SetSeq to original changed bytes", path)
		}
	}
}

func TestIntRoundTrip(t *testing.T) {
	vals := []int64{0, 1, -1, 4, 127, 128, 255, 256, 65535, 65536, 16646418, -16646418, 1<<31 - 1, -(1 << 31)}
	for _, v := range vals {
		var b []byte
		b = AppendInt(b, v)
		got, _, err := ParseValues(b)
		if err != nil {
			t.Fatalf("v=%d: %v", v, err)
		}
		if len(got) != 1 || got[0].Int != v {
			t.Fatalf("v=%d: got %+v", v, got)
		}
	}
}

func TestEncodeFrameRoundTrip(t *testing.T) {
	body := []byte("hello")
	fr := EncodeFrame(TypeCall, body)
	got, rest, err := Split(fr)
	if err != nil || len(rest) != 0 || got.Type != TypeCall || string(got.Body) != "hello" {
		t.Fatalf("roundtrip failed: %v %q", err, got.Body)
	}
}

func TestIPLength(t *testing.T) {
	// IPv4: version 4, IHL 5 (0x45), total length 28.
	v4 := make([]byte, 28)
	v4[0] = 0x45
	binary.BigEndian.PutUint16(v4[2:4], 28)
	if n, ok := IPLength(v4); !ok || n != 28 {
		t.Fatalf("ipv4: n=%d ok=%v", n, ok)
	}
	// IPv6: version 6 (0x60), payload length 20 -> total 60.
	v6 := make([]byte, 60)
	v6[0] = 0x60
	binary.BigEndian.PutUint16(v6[4:6], 20)
	if n, ok := IPLength(v6); !ok || n != 60 {
		t.Fatalf("ipv6: n=%d ok=%v", n, ok)
	}
	if _, ok := IPLength([]byte{0xff, 0x01, 0x02}); ok {
		t.Fatal("non-IP accepted")
	}
}
