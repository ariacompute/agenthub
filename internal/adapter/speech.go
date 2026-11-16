package adapter

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	"agenthub/internal/registry"
)

// Speech transcribes audio to text (STT) and synthesises text to audio (TTS).
//
// The zero value is the deterministic mock: no network, no subprocess, and a
// stable transcript/WAV for the same input, which is what an instance without a
// voice provider keeps using. NewSpeech swaps in an OpenAI compatible audio
// endpoint when one is configured; the two share one signature, so a caller
// never has to branch on which one it holds.
type Speech struct {
	provider speechProvider
}

// speechProvider is the seam a real voice backend implements. Keeping it this
// narrow is what lets the mock stay the default while a provider is optional.
type speechProvider interface {
	Transcribe(ctx context.Context, audio []byte) (string, error)
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// SpeechOptions is everything a real voice backend needs. It is filled from
// Config by the process entry point, so adapter does not import config (the
// dependency would point backwards).
type SpeechOptions struct {
	Mode     string
	Endpoint string
	// APIKeyEnv names the environment variable holding the credential; the
	// value itself is read at request time and never stored.
	APIKeyEnv string
	STTModel  string
	TTSModel  string
	Timeout   time.Duration
	MaxBytes  int64
	Egress    []string
}

// NewSpeech returns the mock when no real endpoint is configured. A real mode
// with a missing endpoint degrades to the mock rather than failing requests:
// startup already warns about it, and a voice note that still transcribes is
// better than an endpoint that refuses every upload.
func NewSpeech(opts SpeechOptions) Speech {
	if strings.TrimSpace(opts.Mode) != registry.ProviderModeOpenAICompatible {
		return Speech{}
	}
	if strings.TrimSpace(opts.Endpoint) == "" {
		return Speech{}
	}
	return Speech{provider: newOpenAISpeech(opts)}
}

// Transcribe returns a transcript for a voice clip.
func (s Speech) Transcribe(ctx context.Context, audio []byte) (string, error) {
	if s.provider != nil {
		return s.provider.Transcribe(ctx, audio)
	}
	return mockTranscribe(ctx, audio)
}

// Synthesize returns audio for a text.
func (s Speech) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if s.provider != nil {
		return s.provider.Synthesize(ctx, text)
	}
	return mockSynthesize(ctx, text)
}

// mockTranscribe derives the transcript from the audio bytes so different
// inputs produce different (stable) text, which keeps tests reproducible
// without a real model.
func mockTranscribe(ctx context.Context, audio []byte) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("empty audio payload")
	}
	sum := crc32.ChecksumIEEE(audio)
	return fmt.Sprintf("Transcribed voice note (sample %08x). The speaker asked the team to continue the plan and report back when ready.", sum), nil
}

// mockSynthesize returns a minimal valid WAV (8 kHz, mono, 16-bit) of soft tones
// derived from the input text. It is small enough to inline as a base64
// PartTypeFile artifact.
func mockSynthesize(ctx context.Context, text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}
	const sampleRate = 8000
	const seconds = 1
	n := sampleRate * seconds
	// 44-byte PCM WAV header + n*2 bytes of samples.
	buf := make([]byte, 44+n*2)
	binary.LittleEndian.PutUint32(buf[0:4], 0x46464952) // "RIFF"
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf)-8))
	binary.LittleEndian.PutUint32(buf[8:12], 0x45564157)  // "WAVE"
	binary.LittleEndian.PutUint32(buf[12:16], 0x20746d66) // "fmt "
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1) // mono
	binary.LittleEndian.PutUint32(buf[24:28], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	binary.LittleEndian.PutUint32(buf[36:40], 0x61746164) // "data"
	binary.LittleEndian.PutUint32(buf[40:44], uint32(n*2))

	for i := 0; i < n; i++ {
		// Gentle descending ramp so the clip is audible but never harsh.
		v := uint16(int32(4000) * int32(((i / 80) % 16)) / 16)
		binary.LittleEndian.PutUint16(buf[44+i*2:], v)
	}
	return buf, nil
}

// DefaultSpeech is the mock speech engine used by the voice endpoint when the
// process has no provider configured.
var DefaultSpeech Speech
