// Package wire implements the Tetrd bulk-stream framing as recovered from
// tetrd 1.3.2 (see ../REA-2026-10-31.md §2 and §3.2).
//
// A frame is:
//
//	[type u8][total len u16 BE][body]
//
// len includes the 3-byte header. type 1 = reply, type 2 = call.
//
// The body is a sequence of TLV values:
//
//	[n u8][tag u8][n-1 bytes]
//
// tag 0x04 is an arbitrarily long big-endian signed integer, tag 0x0c is a
// string (u16 BE length, then bytes). A body may end with an opaque gob tail
// (used by pkg/field for struct arguments and return values); we never parse
// inside it, only replay captured bytes.
//
// Sequence numbers are signed integers, not uint16: the captured daemon emits
// values such as 65422 and 16646418, and the peer echoes them verbatim.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	TypeReply byte = 1
	TypeCall  byte = 2
)

const (
	TagInt    byte = 0x04
	TagString byte = 0x0c
)

// Value is one TLV value. Only Int and Str are populated, according to Tag.
type Value struct {
	Tag byte
	Int int64
	Str string
}

// Frame is a split-out frame. Body excludes the 3-byte header.
type Frame struct {
	Type byte
	Body []byte
}

var ErrShort = errors.New("wire: short frame")

// Split removes one frame from the front of b.
func Split(b []byte) (Frame, []byte, error) {
	if len(b) < 3 {
		return Frame{}, b, ErrShort
	}
	t := b[0]
	if t != TypeReply && t != TypeCall {
		return Frame{}, b, fmt.Errorf("wire: bad frame type %#x", t)
	}
	n := int(binary.BigEndian.Uint16(b[1:3]))
	if n < 3 {
		return Frame{}, b, fmt.Errorf("wire: bad frame length %d", n)
	}
	if n > len(b) {
		return Frame{}, b, ErrShort
	}
	return Frame{Type: t, Body: b[3:n]}, b[n:], nil
}

// EncodeFrame builds a frame around body.
func EncodeFrame(t byte, body []byte) []byte {
	out := make([]byte, 3+len(body))
	out[0] = t
	binary.BigEndian.PutUint16(out[1:3], uint16(3+len(body)))
	copy(out[3:], body)
	return out
}

// ParseValues reads TLV values from the front of body and returns the
// unconsumed remainder (the gob tail, if any).
func ParseValues(body []byte) (vals []Value, tail []byte, err error) {
	i := 0
	for i < len(body) {
		n := int(body[i])
		if n == 0 || i+1+n > len(body) {
			return vals, body[i:], nil
		}
		p := body[i+1 : i+1+n]
		switch p[0] {
		case TagInt:
			v, err := decodeInt(p[1:])
			if err != nil {
				return vals, body[i:], err
			}
			vals = append(vals, Value{Tag: TagInt, Int: v})
		case TagString:
			if len(p) < 3 {
				return vals, body[i:], fmt.Errorf("wire: short string value")
			}
			ln := int(binary.BigEndian.Uint16(p[1:3]))
			if ln != len(p)-3 {
				return vals, body[i:], nil
			}
			vals = append(vals, Value{Tag: TagString, Str: string(p[3:])})
		default:
			return vals, body[i:], nil
		}
		i += 1 + n
	}
	return vals, nil, nil
}

// decodeInt decodes a big-endian two's-complement integer of 1..8 bytes.
func decodeInt(b []byte) (int64, error) {
	if len(b) == 0 || len(b) > 8 {
		return 0, fmt.Errorf("wire: bad int length %d", len(b))
	}
	var v int64
	if b[0]&0x80 != 0 {
		v = -1
	}
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	return v, nil
}

// encodeInt encodes v as big-endian two's-complement, with a minimum of two
// bytes (the daemon always emits small ints as 2 bytes: `03 04 00 02`).
func encodeInt(v int64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	i := 0
	// Strip leading 0x00 (positive) or 0xff (negative) while the sign stays.
	for i < 7 {
		if buf[i] == 0x00 && buf[i+1]&0x80 == 0 {
			i++
			continue
		}
		if buf[i] == 0xff && buf[i+1]&0x80 != 0 {
			i++
			continue
		}
		break
	}
	b := buf[i:]
	if len(b) < 2 {
		if v < 0 {
			return append([]byte{0xff}, b...)
		}
		return append([]byte{0x00}, b...)
	}
	return b
}

// AppendInt appends a TLV integer.
func AppendInt(dst []byte, v int64) []byte {
	b := encodeInt(v)
	dst = append(dst, byte(1+len(b)), TagInt)
	return append(dst, b...)
}

// AppendString appends a TLV string.
func AppendString(dst []byte, s string) []byte {
	dst = append(dst, byte(3+len(s)), TagString)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(s)))
	dst = append(dst, l[:]...)
	return append(dst, s...)
}

// CallHead builds the header body for an outgoing call: (0, seq, method).
func CallHead(seq int64, method string) []byte {
	var b []byte
	b = AppendInt(b, 0)
	b = AppendInt(b, seq)
	return AppendString(b, method)
}

// ReplyHead builds the header body for a reply: (4, seq, method, "nil").
func ReplyHead(seq int64, method string) []byte {
	var b []byte
	b = AppendInt(b, 4)
	b = AppendInt(b, seq)
	b = AppendString(b, method)
	return AppendString(b, "nil")
}

// ReplyValue builds a value body with no return values: (6, seq, 0).
func ReplyValue(seq int64) []byte {
	var b []byte
	b = AppendInt(b, 6)
	b = AppendInt(b, seq)
	return AppendInt(b, 0)
}

// IPLength returns the length of the IP datagram at the front of b when b
// starts with an IPv4/IPv6 header, and whether it is usable.
func IPLength(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	switch b[0] >> 4 {
	case 4:
		if len(b) < 4 {
			return 0, false
		}
		n := int(binary.BigEndian.Uint16(b[2:4]))
		if n < 20 || n > len(b) {
			return 0, false
		}
		return n, true
	case 6:
		if len(b) < 6 {
			return 0, false
		}
		n := 40 + int(binary.BigEndian.Uint16(b[4:6]))
		if n > len(b) {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// ReplyInt builds a value body returning a single int: (6, seq, 2, type, v).
func ReplyInt(seq int64, typ string, v int64) []byte {
	var b []byte
	b = AppendInt(b, 6)
	b = AppendInt(b, seq)
	b = AppendInt(b, 2)
	b = AppendString(b, typ)
	return AppendInt(b, v)
}

// Args builds an args body with no arguments: (2, seq, 0).
func Args(seq int64) []byte {
	var b []byte
	b = AppendInt(b, 2)
	b = AppendInt(b, seq)
	return AppendInt(b, 0)
}

// ArgsInt builds an args body with one named int argument.
func ArgsInt(seq int64, typ string, v int64) []byte {
	var b []byte
	b = AppendInt(b, 2)
	b = AppendInt(b, seq)
	b = AppendInt(b, 2)
	b = AppendString(b, typ)
	return AppendInt(b, v)
}

// SetSeq returns a copy of frame with its second TLV value (the sequence
// number) replaced by seq. The frame length and the value's own length byte
// are recomputed, so the replacement may be shorter or longer than the
// original (captured fixtures carry small seqs; live traffic carries values
// such as 16646418).
func SetSeq(frame []byte, seq int64) ([]byte, error) {
	if len(frame) < 3 {
		return nil, ErrShort
	}
	typ := frame[0]
	if typ != TypeReply && typ != TypeCall {
		return nil, fmt.Errorf("wire: bad frame type %#x", typ)
	}
	body := frame[3:]
	// First TLV: the body kind. Skip over it verbatim.
	if len(body) < 2 {
		return nil, ErrShort
	}
	end1 := 1 + int(body[0])
	if end1 >= len(body) {
		return nil, ErrShort
	}
	// Second TLV: the sequence number.
	end2 := end1 + 1 + int(body[end1])
	if end2 > len(body) {
		return nil, ErrShort
	}
	enc := encodeInt(seq)
	if 1+len(enc) > 255 {
		return nil, fmt.Errorf("wire: seq too long")
	}
	newBody := make([]byte, 0, len(body)-(end2-end1)+1+len(enc))
	newBody = append(newBody, body[:end1]...)
	newBody = append(newBody, byte(1+len(enc)), TagInt)
	newBody = append(newBody, enc...)
	newBody = append(newBody, body[end2:]...)
	if len(newBody)+3 > 0xffff {
		return nil, fmt.Errorf("wire: frame too long")
	}
	out := make([]byte, 3+len(newBody))
	out[0] = typ
	binary.BigEndian.PutUint16(out[1:3], uint16(3+len(newBody)))
	copy(out[3:], newBody)
	return out, nil
}

// SeqOf returns the sequence number of a frame, if it has one.
func SeqOf(frame []byte) (int64, bool) {
	if len(frame) < 3 {
		return 0, false
	}
	body := frame[3:]
	if len(body) < 2 {
		return 0, false
	}
	end1 := 1 + int(body[0])
	if end1 >= len(body) {
		return 0, false
	}
	p := body[end1+1 : end1+1+int(body[end1])]
	if len(p) < 1 || p[0] != TagInt {
		return 0, false
	}
	v, err := decodeInt(p[1:])
	if err != nil {
		return 0, false
	}
	return v, true
}
