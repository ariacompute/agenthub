package adapter

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestTranscribeIsDeterministic(t *testing.T) {
	var s Speech
	out, err := s.Transcribe(context.Background(), []byte("voice-bytes-1"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !strings.Contains(out, "Transcribed voice note") {
		t.Fatalf("unexpected transcript: %q", out)
	}
	// The same bytes must always yield the same transcript so tests are stable.
	again, _ := s.Transcribe(context.Background(), []byte("voice-bytes-1"))
	if again != out {
		t.Fatalf("Transcribe is not deterministic: %q != %q", out, again)
	}
	// Different bytes produce different text.
	other, _ := s.Transcribe(context.Background(), []byte("different-clip"))
	if other == out {
		t.Fatalf("different input produced identical transcript")
	}
	// An empty clip is an error rather than a blank transcript.
	if _, err := s.Transcribe(context.Background(), nil); err == nil {
		t.Fatal("empty audio must error")
	}
}

func TestSynthesizeProducesValidWAV(t *testing.T) {
	var s Speech
	wav, err := s.Synthesize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	// 44-byte PCM WAV header plus one second at 8kHz/16-bit mono.
	const expected = 44 + 8000*2
	if len(wav) != expected {
		t.Fatalf("wav length = %d, want %d", len(wav), expected)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE container: % x", wav[0:12])
	}
	if string(wav[12:16]) != "fmt " {
		t.Fatalf("missing fmt chunk")
	}
	if sr := binary.LittleEndian.Uint32(wav[24:28]); sr != 8000 {
		t.Fatalf("sample rate = %d, want 8000", sr)
	}
	if ch := binary.LittleEndian.Uint16(wav[22:24]); ch != 1 {
		t.Fatalf("channels = %d, want mono", ch)
	}
	if bits := binary.LittleEndian.Uint16(wav[34:36]); bits != 16 {
		t.Fatalf("bits = %d, want 16", bits)
	}
	if _, err := s.Synthesize(context.Background(), ""); err == nil {
		t.Fatal("empty text must error")
	}
}
