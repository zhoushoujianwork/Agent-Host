package feishu

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	pb "github.com/zhoushoujianwork/Agent-Host/plugins/feishu/internal/audioproto"
	"google.golang.org/protobuf/proto"
)

// Wire constants come from the user-supplied reference client. They are
// source-verified only; no live Feishu session has confirmed them yet.
const (
	audioFrontierService int32 = 33555721
	audioFrontierMethod  int32 = 1
	audioPayloadEncoding       = "binary"
	audioPayloadType           = "application/x-protobuf"
	audioFrameNormal     int32 = 0

	audioMediaType     = "audio/pcm"
	audioMediaEncoding = "s16le"
	audioSampleRate    = 24000

	// Largest binary WebSocket message accepted in either direction.
	maxAudioMessage = 1 << 20
	// Largest PCM payload of one upstream append event (100 ms at 24 kHz).
	maxUpstreamChunk = 4800
	// Largest PCM accepted by one WriteAudio call.
	maxWritePCM = 1 << 20
	// Consecutive ack/cursor/empty frames tolerated by one read.
	maxSkippedFrames = 1024
	maxAudioLabel    = 256
)

// Test seams: production uses the wall clock and random event identifiers.
var (
	audioClock = time.Now
	audioID    = randomAudioID
)

func randomAudioID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("Feishu audio: system randomness unavailable")
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func audioTimestamp() string { return audioClock().UTC().Format(time.RFC3339) }

func skippedFrameType(t int32) bool { return t == 1 || t == 2 || t == 16 || t == 32 }

func audioFormatMessage() *pb.AudioFormat {
	return &pb.AudioFormat{Type: audioMediaType, Encoding: audioMediaEncoding, SampleRate: audioSampleRate}
}

func sessionCreateEvent(id string) *pb.ClientEvent {
	format := audioFormatMessage()
	return &pb.ClientEvent{
		Type: "session.create", EventId: id, CreatedAt: audioTimestamp(),
		Payload: &pb.ClientEvent_SessionCreate{SessionCreate: &pb.SessionCreate{
			Session: &pb.Session{Media: &pb.Media{AudioUpstreamFormat: format, AudioDownstreamFormat: audioFormatMessage()}},
		}},
	}
}

func audioAppendEvent(session int64, pcm []byte) *pb.ClientEvent {
	return &pb.ClientEvent{
		Type: "audio.upstream.append", SessionId: session, CreatedAt: audioTimestamp(),
		Payload: &pb.ClientEvent_AudioUpstreamAppend{AudioUpstreamAppend: &pb.AudioUpstreamAppend{Delta: pcm}},
	}
}

func audioClearEvent(session int64, id string) *pb.ClientEvent {
	return &pb.ClientEvent{
		Type: "audio.upstream.clear", EventId: id, SessionId: session, CreatedAt: audioTimestamp(),
		Payload: &pb.ClientEvent_AudioUpstreamClear{AudioUpstreamClear: &pb.AudioUpstreamClear{}},
	}
}

func sessionCloseEvent(session int64, id string) *pb.ClientEvent {
	return &pb.ClientEvent{
		Type: "session.close", EventId: id, SessionId: session, CreatedAt: audioTimestamp(),
		Payload: &pb.ClientEvent_SessionClose{SessionClose: &pb.SessionClose{Reason: pb.ClientCloseReason_USER_LEFT}},
	}
}

// encodeAudioFrame wraps a client event in the Frontier frame. Optional fields
// that the reference client always sets are set explicitly (even when empty).
func encodeAudioFrame(seq uint64, event *pb.ClientEvent) ([]byte, error) {
	payload, err := proto.Marshal(event)
	if err != nil {
		return nil, errors.New("Feishu audio event could not be encoded")
	}
	frame := &pb.Frame{
		SeqID: proto.Uint64(seq), LogID: proto.Uint64(0),
		Service: proto.Int32(audioFrontierService), Method: proto.Int32(audioFrontierMethod),
		PayloadEncoding: proto.String(audioPayloadEncoding), PayloadType: proto.String(audioPayloadType),
		Payload: payload, LogIDNew: proto.String(""), MsgId: proto.String(event.EventId),
		FrameType: proto.Int32(audioFrameNormal),
	}
	out, err := proto.Marshal(frame)
	if err != nil || len(out) > maxAudioMessage {
		return nil, errors.New("Feishu audio frame could not be encoded")
	}
	return out, nil
}

// decodeAudioFrame returns a nil event for ack, cursor and empty-payload frames.
// Unknown frame types and malformed protobuf are errors; no payload is echoed.
func decodeAudioFrame(raw []byte) (*pb.ServerEvent, error) {
	if len(raw) == 0 || len(raw) > maxAudioMessage {
		return nil, errors.New("Feishu audio frame has an invalid size")
	}
	var frame pb.Frame
	if proto.Unmarshal(raw, &frame) != nil {
		return nil, errors.New("Feishu audio frame is malformed")
	}
	switch kind := frame.GetFrameType(); {
	case skippedFrameType(kind):
		return nil, nil
	case kind != audioFrameNormal:
		return nil, errors.New("Feishu audio frame has an unsupported type")
	}
	if len(frame.GetPayload()) == 0 {
		return nil, nil
	}
	var event pb.ServerEvent
	if proto.Unmarshal(frame.GetPayload(), &event) != nil {
		return nil, errors.New("Feishu audio event is malformed")
	}
	return &event, nil
}

func closeReasonName(r pb.ServerCloseReason) string { return r.String() }

func protocolError(what string) error { return fmt.Errorf("Feishu audio protocol violation: %s", what) }
