package feishumeeting

import (
	"context"
	"errors"
	"sync"

	feishulib "github.com/zhoushoujianwork/Agent-Host/plugins/feishu"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech"
	"github.com/zhoushoujianwork/Agent-Host/plugins/speech/doubao"
)

// FeishuAudioOpener is the production AudioOpener. It makes one attempt per
// Open through MeetingClient.OpenAudio and never reconnects.
type FeishuAudioOpener struct {
	Config    Config
	Transport Client
}

func (o FeishuAudioOpener) Open(ctx context.Context, meetingID string) (AudioSession, error) {
	c := o.Config
	c.MeetingID = meetingID
	client, err := meetingClient(c, o.Transport)
	if err != nil {
		return nil, err
	}
	session, err := client.OpenAudio(ctx, meetingID, feishulib.AudioConfig{SampleRate: audioRate})
	if err != nil {
		return nil, err
	}
	pump, cancel := context.WithCancel(context.Background())
	a := &feishuAudio{session: session, cancel: cancel, frames: make(chan AudioFrame, 32), errs: make(chan error, 1), done: make(chan struct{})}
	go a.read(pump)
	return a, nil
}

type feishuAudio struct {
	session  *feishulib.AudioSession
	cancel   context.CancelFunc
	frames   chan AudioFrame
	errs     chan error
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func (a *feishuAudio) read(ctx context.Context) {
	defer close(a.done)
	// Errors closes after Frames; a terminal error is buffered before either
	// closes, so it survives the consumer seeing Frames closed first.
	defer close(a.errs)
	defer close(a.frames)
	for {
		frame, err := a.session.ReadAudio(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.errs <- err // buffered; read ends after its first error
			}
			return
		}
		track := frame.TrackID
		if track == "" {
			track = frame.Source
		}
		select {
		case a.frames <- AudioFrame{Track: track, PCM: frame.PCM}:
		case <-ctx.Done():
			return
		}
	}
}

func (a *feishuAudio) Frames() <-chan AudioFrame { return a.frames }
func (a *feishuAudio) Errors() <-chan error      { return a.errs }
func (a *feishuAudio) Play(ctx context.Context, pcm []byte) error {
	return a.session.WriteAudio(ctx, pcm)
}
func (a *feishuAudio) ClearPlayback(ctx context.Context) error { return a.session.ClearAudio(ctx) }

func (a *feishuAudio) Close() error {
	a.once.Do(func() {
		a.closeErr = a.session.Close() // sends the close notice, then unblocks reads
		a.cancel()
		<-a.done
	})
	return a.closeErr
}

// DoubaoSpeech builds ASR and TTS providers from validated configuration and
// resolved credentials. It performs no network access.
func DoubaoSpeech(v VoiceConfig, c VoiceCredentials) (speech.ASRProvider, speech.TTSProvider, error) {
	if err := v.Validate(); err != nil {
		return nil, nil, err
	}
	if !v.Enabled {
		return nil, nil, errors.New("Feishu voice is not enabled")
	}
	asr := doubao.Config{ASREndpoint: v.ASR.Endpoint, APIKeyV2: c.ASRAPIKey, AppID: c.ASRAppID, AccessToken: c.ASRAccessToken, ASRCluster: v.ASR.Cluster, ResourceID: v.ASR.ResourceID}
	tts := doubao.Config{TTSEndpoint: v.TTS.Endpoint, AppID: c.TTSAppID, AccessToken: c.TTSAccessToken, TTSCluster: v.TTS.Cluster}
	return doubao.ASRProvider{Config: asr}, doubao.TTSProvider{Config: tts}, nil
}
