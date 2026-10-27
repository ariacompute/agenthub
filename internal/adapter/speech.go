package adapter

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// Speech transcribes audio to text (STT) and synthesises text to audio (TTS). The
// first release ships a deterministic mock so the whole voice path runs offline with
// no network and no subprocess, matching the other five adapters.
//
// TODO(real): replace Transcribe/Synthesize with a real provider behind the same
// signature (e.g. an OpenAI-compatible audio endpoint configured via KindConfig),
// reading credentials from env vars by name, never inline.
type Speech struct{}

// Transcribe returns a deterministic transcript for a voice clip. The transcript is
// derived from the audio bytes so different inputs produce different (stable) text,
// which keeps tests reproducible without a real model.
func (Speech) Transcribe(ctx context.Context, audio []byte) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("empty audio payload")
	}
	sum := crc32.ChecksumIEEE(audio)
	return fmt.Sprintf("Transcribed voice note (sample %08x). The speaker asked the team to continue the plan and report back when ready.", sum), nil
}

// Synthesize returns a minimal valid WAV (8 kHz, mono, 16-bit) of soft tones derived
// from the input text. It is small enough to inline as a base64 PartTypeFile
// artifact; a real provider should return a URI instead and keep Name/MimeType.
func (Speech) Synthesize(ctx context.Context, text string) ([]byte, error) {
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

// DefaultSpeech is the mock speech engine used by the voice endpoint.
var DefaultSpeech Speech
