package doubao

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestProtocolFramingAndBounds(t *testing.T) {
	for _, f := range []frame{
		{kind: fullRequest, serialization: 1, compression: 1, payload: []byte("json")},
		{kind: audioRequest, flags: 2, payload: nil},
		{kind: audioResponse, flags: 1, sequence: 1, payload: []byte{0, 1}},
		{kind: audioResponse, flags: 2, sequence: -1, payload: []byte{4, 5}},
		{kind: audioResponse, flags: 3, sequence: -2, payload: []byte{2, 3}},
		{kind: errorResponse, code: 45000000, payload: []byte("private remote error")},
	} {
		b := encode(f)
		got, err := decode(b)
		if err != nil || got.kind != f.kind || got.flags != f.flags || got.sequence != f.sequence || got.code != f.code || !bytes.Equal(got.payload, f.payload) {
			t.Fatalf("roundtrip: %+v %v", got, err)
		}
		for i := 0; i < len(b); i++ {
			if _, err := decode(b[:i]); err == nil {
				t.Fatalf("accepted truncation at %d", i)
			}
		}
	}
	good := encode(frame{kind: fullResponse, serialization: 1, payload: []byte(`{}`)})
	for _, mutate := range []func([]byte){
		func(b []byte) { b[0] = 0x21 }, func(b []byte) { b[0] = 0x10 }, func(b []byte) { b[0] = 0x1f },
		func(b []byte) { b[1] = 0x94 }, func(b []byte) { b[1] = 0x80 }, func(b []byte) { b[2] = 0x20 },
		func(b []byte) { b[2] = 0x12 }, func(b []byte) { b[3] = 1 },
		func(b []byte) { binary.BigEndian.PutUint32(b[4:], 0xffffffff) },
	} {
		b := bytes.Clone(good)
		mutate(b)
		if _, err := decode(b); err == nil {
			t.Fatalf("accepted malformed %x", b)
		}
	}
	if _, err := decode(make([]byte, maxMessage+1)); err == nil {
		t.Fatal("accepted oversized message")
	}
	if _, err := decode(append(good, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
}

func TestDecompressionBounds(t *testing.T) {
	for _, size := range []int{0, 2, maxMessage, maxMessage + 1} {
		original := bytes.Repeat([]byte{1}, size)
		b, err := compress(original)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decompress(b, 1)
		if size > maxMessage {
			if err == nil {
				t.Fatal("accepted gzip bomb")
			}
			continue
		}
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("size %d: %v", size, err)
		}
		if len(b) > 5 {
			b[len(b)-5] ^= 0xff
			if _, err := decompress(b, 1); err == nil {
				t.Fatal("accepted invalid checksum")
			}
		}
	}
	for _, v := range []struct {
		b []byte
		c byte
	}{{[]byte("bad gzip"), 1}, {nil, 2}, {make([]byte, maxMessage+1), 0}} {
		if _, err := decompress(v.b, v.c); err == nil {
			t.Fatal("accepted invalid compressed payload")
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(encode(frame{kind: fullResponse, serialization: 1, payload: []byte(`{}`)}))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = decode(b) })
}

func TestLegacyTTSRawFraming(t *testing.T) {
	for _, ack := range [][]byte{
		{0x11, 0xb0, 0x00, 0x00},
		{0x11, 0xb0, 0x00, 0x00, 0, 0, 0, 0},
	} {
		got, err := decode(ack)
		if err != nil || got.kind != audioResponse || got.flags != 0 || len(got.payload) != 0 {
			t.Fatalf("ACK: %+v %v", got, err)
		}
	}
	// These bytes are independent of the encoder: flags2 has a signed sequence.
	final := []byte{0x11, 0xb2, 0, 0, 0xff, 0xff, 0xff, 0xfe, 0, 0, 0, 2, 5, 6}
	got, err := decode(final)
	if err != nil || got.sequence != -2 || !got.final() || !bytes.Equal(got.payload, []byte{5, 6}) {
		t.Fatalf("flags2 final: %+v %v", got, err)
	}
	for _, bad := range [][]byte{
		{0x11, 0xb0, 0x10, 0},                                  // JSON ACK is unsupported.
		{0x11, 0xb0, 0x01, 0},                                  // Compressed ACK is unsupported.
		{0x11, 0xb0, 0, 0, 0, 0, 0, 2, 1, 2},                   // ACK cannot carry audio.
		{0x11, 0xb1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},             // zero positive sequence
		{0x11, 0xb1, 0, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, // negative nonfinal
		{0x11, 0xb2, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0},             // positive final
		{0x11, 0xb3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},             // zero final
		{0x11, 0xb2, 0, 0, 0, 0, 0, 0},                         // missing sequence/length
	} {
		if _, err := decode(bad); err == nil {
			t.Fatalf("accepted malformed legacy TTS: %x", bad)
		}
	}
	// ASR input completion is still size-only with no signed sequence.
	got, err = decode([]byte{0x11, 0x22, 0, 0, 0, 0, 0, 0})
	if err != nil || got.kind != audioRequest || got.sequence != 0 || got.flags != 2 {
		t.Fatalf("ASR input completion: %+v %v", got, err)
	}
}
