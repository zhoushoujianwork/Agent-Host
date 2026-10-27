package feishumeeting

import (
	"context"
	"encoding/binary"
	"math"
)

// Audio contract used by the voice bridge. Input is 24 kHz signed little-endian
// mono PCM16; playback is the same format. Production adapters live in the CLI
// package so this package stays testable with fakes.
const (
	audioRate      = 24000
	asrRate        = 16000
	vadFrameMS     = 20
	vadFrameInput  = audioRate * vadFrameMS / 1000 // 480 samples at 24 kHz
	vadFrameSample = asrRate * vadFrameMS / 1000   // 320 samples at 16 kHz
	vadPreroll     = 5                             // 100 ms kept before detected speech
	vadMinSpeech   = 10                            // 200 ms
	vadEndSilence  = 30                            // 600 ms
	vadMaxFrames   = 750                           // 15 s
	vadThreshold   = 500                           // RMS on a 32768 full scale
)

// AudioFrame is one bounded chunk from a meeting audio track. Track separates
// buffers only; it is never a speaker identity.
type AudioFrame struct {
	Track string
	PCM   []byte
}

// AudioSession is one connected meeting audio channel. Frames and Errors are
// closed by the session when it ends. Close is idempotent and immediate.
type AudioSession interface {
	Frames() <-chan AudioFrame
	Errors() <-chan error
	Play(ctx context.Context, pcm []byte) error
	// ClearPlayback discards audio already queued by Play.
	ClearPlayback(ctx context.Context) error
	Close() error
}

type AudioOpener interface {
	Open(ctx context.Context, meetingID string) (AudioSession, error)
}

// resampler24to16 converts 24 kHz to 16 kHz (exact 3:2) with linear
// interpolation. Its state is the sample position, so results do not depend on
// how input is split into chunks, including odd byte boundaries.
type resampler24to16 struct {
	carry    byte
	hasCarry bool
	in, out  int64
	prev     int16
}

func (r *resampler24to16) Write(p []byte) []byte {
	dst := make([]byte, 0, len(p)/3*2+4)
	if r.hasCarry && len(p) > 0 {
		dst = r.sample(dst, int16(binary.LittleEndian.Uint16([]byte{r.carry, p[0]})))
		r.hasCarry, p = false, p[1:]
	}
	for len(p) >= 2 {
		dst = r.sample(dst, int16(binary.LittleEndian.Uint16(p)))
		p = p[2:]
	}
	if len(p) == 1 {
		r.carry, r.hasCarry = p[0], true
	}
	return dst
}

func (r *resampler24to16) sample(dst []byte, x int16) []byte {
	// Output k lies at input position 1.5k and needs sample ceil(1.5k).
	if need := (r.out*3 + 1) / 2; need == r.in {
		y := x
		if r.out*3%2 == 1 {
			y = int16((int32(r.prev) + int32(x)) / 2)
		}
		dst = binary.LittleEndian.AppendUint16(dst, uint16(y))
		r.out++
	}
	r.prev = x
	r.in++
	return dst
}

// segmenter is a bounded energy VAD over 16 kHz PCM. It emits completed
// utterances only; partial speech is never released.
type segmenter struct {
	partial []int16
	preroll [][]int16
	active  bool
	frames  int
	speech  int
	silence int
	buf     []byte
}

func (s *segmenter) Reset() { *s = segmenter{} }

func (s *segmenter) Write(pcm []byte) (utterances [][]byte) {
	for len(pcm) >= 2 {
		s.partial = append(s.partial, int16(binary.LittleEndian.Uint16(pcm)))
		pcm = pcm[2:]
		if len(s.partial) == vadFrameSample {
			if u := s.frame(s.partial); u != nil {
				utterances = append(utterances, u)
			}
			s.partial = s.partial[:0]
		}
	}
	return utterances
}

func (s *segmenter) frame(f []int16) []byte {
	var sum float64
	for _, x := range f {
		sum += float64(x) * float64(x)
	}
	voiced := math.Sqrt(sum/float64(len(f))) >= vadThreshold
	if !s.active {
		if !voiced {
			s.preroll = append(s.preroll, append([]int16(nil), f...))
			if len(s.preroll) > vadPreroll {
				s.preroll = s.preroll[1:]
			}
			return nil
		}
		s.active, s.speech, s.silence, s.frames = true, 0, 0, 0
		for _, p := range s.preroll {
			s.buf = appendPCM(s.buf, p)
			s.frames++
		}
		s.preroll = nil
	}
	s.buf = appendPCM(s.buf, f)
	s.frames++
	if voiced {
		s.speech++
		s.silence = 0
	} else {
		s.silence++
	}
	if s.silence >= vadEndSilence || s.frames >= vadMaxFrames {
		var out []byte
		if s.speech >= vadMinSpeech {
			out = s.buf
		}
		s.active, s.buf, s.frames, s.speech, s.silence = false, nil, 0, 0, 0
		return out
	}
	return nil
}

func appendPCM(dst []byte, samples []int16) []byte {
	for _, x := range samples {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(x))
	}
	return dst
}
