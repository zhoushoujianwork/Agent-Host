package feishumeeting

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func samplesOf(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

func TestResamplerLinearRamp(t *testing.T) {
	// Input x[i] = 100*i at 24 kHz; output k sits at input position 1.5k, so it is 150*k.
	in := make([]int16, 300)
	for i := range in {
		in[i] = int16(100 * i)
	}
	var r resampler24to16
	got := samplesOf(r.Write(appendPCM(nil, in)))
	if len(got) != 200 {
		t.Fatalf("300 samples at 24 kHz produced %d samples, want 200", len(got))
	}
	for k, y := range got {
		if y != int16(150*k) {
			t.Fatalf("output %d = %d, want %d", k, y, 150*k)
		}
	}
}

func TestResamplerIsChunkAndOddByteIndependent(t *testing.T) {
	in := make([]int16, 1203)
	for i := range in {
		in[i] = int16((i*7919)%20000 - 10000)
	}
	pcm := appendPCM(nil, in)
	var whole resampler24to16
	want := whole.Write(pcm)

	for _, size := range []int{1, 2, 3, 5, 479, 480, 481, 960, 1001} {
		var r resampler24to16
		var got []byte
		for off := 0; off < len(pcm); off += size {
			got = append(got, r.Write(pcm[off:min(off+size, len(pcm))])...)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("chunk size %d changed the resampled output (%d vs %d bytes)", size, len(got), len(want))
		}
	}
	// A dangling byte is carried, not dropped or output.
	var r resampler24to16
	if out := r.Write([]byte{0x34}); len(out) != 0 || !r.hasCarry {
		t.Fatalf("single byte output %d bytes, carry=%v", len(out), r.hasCarry)
	}
	if out := r.Write([]byte{0x12}); !bytes.Equal(out, []byte{0x34, 0x12}) {
		t.Fatalf("carried sample = %x", out)
	}
}

// feed writes n frames of constant amplitude and returns every utterance.
func feed(s *segmenter, frames int, amp int16) (out [][]byte) {
	return s.Write(pcm16k(frames, amp))
}

func TestSegmenterMinSpeech(t *testing.T) {
	var s segmenter
	feed(&s, vadMinSpeech-1, 3000)
	if u := feed(&s, vadEndSilence, 0); len(u) != 0 {
		t.Fatalf("%d voiced frames released an utterance, want fewer than %d", vadMinSpeech-1, vadMinSpeech)
	}
	feed(&s, vadMinSpeech, 3000)
	u := feed(&s, vadEndSilence, 0)
	if len(u) != 1 {
		t.Fatalf("got %d utterances, want 1", len(u))
	}
	if want := (vadMinSpeech + vadEndSilence) * vadFrameSample * 2; len(u[0]) != want {
		t.Fatalf("utterance is %d bytes, want %d", len(u[0]), want)
	}
}

func TestSegmenterEndSilenceAndPreroll(t *testing.T) {
	var s segmenter
	feed(&s, 8, 0) // only the last vadPreroll frames are kept
	feed(&s, vadMinSpeech, 3000)
	if u := feed(&s, vadEndSilence-1, 0); len(u) != 0 {
		t.Fatal("utterance released before the end silence elapsed")
	}
	u := feed(&s, 1, 0)
	if len(u) != 1 {
		t.Fatalf("got %d utterances after end silence, want 1", len(u))
	}
	if want := (vadPreroll + vadMinSpeech + vadEndSilence) * vadFrameSample * 2; len(u[0]) != want {
		t.Fatalf("utterance is %d bytes, want %d", len(u[0]), want)
	}
	// State was reset: the next utterance is independent.
	feed(&s, vadMinSpeech, 3000)
	if u := feed(&s, vadEndSilence, 0); len(u) != 1 {
		t.Fatalf("second utterance count = %d, want 1", len(u))
	}
}

func TestSegmenterMaxLength(t *testing.T) {
	var s segmenter
	if u := feed(&s, vadMaxFrames-1, 3000); len(u) != 0 {
		t.Fatal("utterance released before the maximum length")
	}
	u := feed(&s, 1, 3000)
	if len(u) != 1 || len(u[0]) != vadMaxFrames*vadFrameSample*2 {
		t.Fatalf("max-length utterance = %d utterances, %d bytes", len(u), func() int {
			if len(u) == 0 {
				return 0
			}
			return len(u[0])
		}())
	}
}

func TestSegmenterSplitWritesAndReset(t *testing.T) {
	pcm := append(pcm16k(vadMinSpeech, 3000), pcm16k(vadEndSilence, 0)...)
	var s segmenter
	var got [][]byte
	for off := 0; off < len(pcm); off += 100 { // even splits inside and across frames
		got = append(got, s.Write(pcm[off:min(off+100, len(pcm))])...)
	}
	if len(got) != 1 {
		t.Fatalf("split writes produced %d utterances, want 1", len(got))
	}

	var r segmenter
	feed(&r, vadMinSpeech, 3000)
	r.Reset()
	if u := feed(&r, vadEndSilence+vadMinSpeech, 0); len(u) != 0 {
		t.Fatal("Reset kept speech from before the reset")
	}
}
