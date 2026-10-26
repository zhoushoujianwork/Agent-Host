package doubao

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
)

const (
	maxMessage         = 1 << 20
	maxTTSOutput       = 8 << 20
	maxAudioChunk      = 12800
	fullRequest   byte = 1
	audioRequest  byte = 2
	fullResponse  byte = 9
	audioResponse byte = 11
	errorResponse byte = 15
)

type frame struct {
	kind, flags, serialization, compression byte
	sequence                                int32
	code                                    uint32
	payload                                 []byte
}

func encode(f frame) []byte {
	extra := 0
	if f.kind == audioResponse && f.flags == 0 && len(f.payload) == 0 {
		return []byte{0x11, 0xb0, f.serialization<<4 | f.compression, 0}
	}
	if f.kind == errorResponse || f.flags == 1 || f.flags == 3 || (f.kind == audioResponse && f.flags == 2) {
		extra = 4
	}
	b := make([]byte, 8+extra+len(f.payload))
	b[0], b[1], b[2] = 0x11, f.kind<<4|f.flags, f.serialization<<4|f.compression
	offset := 4
	if extra != 0 {
		n := uint32(f.sequence)
		if f.kind == errorResponse {
			n = f.code
		}
		binary.BigEndian.PutUint32(b[offset:], n)
		offset += 4
	}
	binary.BigEndian.PutUint32(b[offset:], uint32(len(f.payload)))
	copy(b[offset+4:], f.payload)
	return b
}

func decode(b []byte) (frame, error) {
	bad := errors.New("doubao: malformed binary frame")
	if len(b) < 4 || len(b) > maxMessage || b[0]>>4 != 1 || b[3] != 0 {
		return frame{}, bad
	}
	offset := int(b[0]&15) * 4
	if offset < 4 || offset > len(b) {
		return frame{}, bad
	}
	f := frame{kind: b[1] >> 4, flags: b[1] & 15, serialization: b[2] >> 4, compression: b[2] & 15}
	if f.flags > 3 || f.serialization > 1 || f.compression > 1 {
		return frame{}, bad
	}
	switch f.kind {
	case fullRequest, audioRequest, fullResponse, audioResponse, errorResponse:
	default:
		return frame{}, bad
	}
	// Legacy TTS ACKs have only a header; accept the earlier zero-length
	// payload spelling as well. ASR input completion retains its own framing.
	if f.kind == audioResponse && f.flags == 0 {
		if f.serialization != 0 || f.compression != 0 {
			return frame{}, bad
		}
		if len(b) == offset || (len(b) == offset+4 && binary.BigEndian.Uint32(b[offset:]) == 0) {
			return f, nil
		}
		return frame{}, bad
	}
	if offset > len(b)-4 {
		return frame{}, bad
	}
	if f.kind == errorResponse || f.flags == 1 || f.flags == 3 || (f.kind == audioResponse && f.flags == 2) {
		if offset > len(b)-8 {
			return frame{}, bad
		}
		if f.kind == errorResponse {
			f.code = binary.BigEndian.Uint32(b[offset:])
		} else {
			f.sequence = int32(binary.BigEndian.Uint32(b[offset:]))
		}
		offset += 4
	}
	if f.kind == audioResponse && ((f.flags == 1 && f.sequence <= 0) || (f.flags >= 2 && f.sequence >= 0)) {
		return frame{}, bad
	}
	n := uint64(binary.BigEndian.Uint32(b[offset:]))
	offset += 4
	if n != uint64(len(b)-offset) {
		return frame{}, bad
	}
	f.payload = b[offset:]
	return f, nil
}

func compress(b []byte) ([]byte, error) {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decompress(b []byte, compression byte) ([]byte, error) {
	if len(b) > maxMessage {
		return nil, errors.New("doubao: payload exceeds 1 MiB")
	}
	if compression == 0 {
		return b, nil
	}
	if compression != 1 {
		return nil, errors.New("doubao: unsupported compression")
	}
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("doubao: malformed gzip payload")
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxMessage+1))
	if err != nil {
		return nil, errors.New("doubao: malformed gzip payload")
	}
	if len(out) > maxMessage {
		return nil, errors.New("doubao: decompressed payload exceeds 1 MiB")
	}
	return out, nil
}

func (f frame) final() bool { return f.flags == 2 || f.flags == 3 }
