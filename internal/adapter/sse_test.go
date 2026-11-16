package adapter

import (
	"errors"
	"strings"
	"testing"
)

func readAllFrames(t *testing.T, input string, limit int) ([]sseFrame, error) {
	t.Helper()
	decoder := newSSEDecoderLimit(strings.NewReader(input), limit)
	var frames []sseFrame
	for {
		event, data, ok, err := decoder.Next()
		if err != nil {
			return frames, err
		}
		if !ok {
			return frames, nil
		}
		frames = append(frames, sseFrame{event: event, data: data})
	}
}

func TestSSEDecoderReadsEvents(t *testing.T) {
	frames, err := readAllFrames(t, ""+
		"event: delta\n"+
		"data: one\n"+
		"data: two\n"+
		"\n"+
		": keepalive\n"+
		"\n"+
		"data: three\n"+
		"\n"+
		"data: [DONE]\n"+
		"\n", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2: %+v", len(frames), frames)
	}
	if frames[0].event != "delta" || frames[0].data != "one\ntwo" {
		t.Fatalf("first frame = %+v", frames[0])
	}
	// A data line without an event name is the default "message" event.
	if frames[1].event != "message" || frames[1].data != "three" {
		t.Fatalf("second frame = %+v", frames[1])
	}
}

func TestSSEDecoderDeliversAnUnterminatedFrame(t *testing.T) {
	frames, err := readAllFrames(t, "data: tail", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 || frames[0].data != "tail" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestSSEDecoderIgnoresEmptyInput(t *testing.T) {
	frames, err := readAllFrames(t, "", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("frames = %+v, want none", frames)
	}
}

func TestSSEDecoderIgnoresUnknownFields(t *testing.T) {
	frames, err := readAllFrames(t, "id: 7\nretry: 100\ndata: payload\n\n", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 || frames[0].data != "payload" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestSSEDecoderEnforcesTheStreamLimit(t *testing.T) {
	_, err := readAllFrames(t, "data: abcdef\n\n", 8)
	if !errors.Is(err, errStreamTooLarge) {
		t.Fatalf("err = %v, want errStreamTooLarge", err)
	}
}

func TestSSEDecoderEnforcesTheLineLimit(t *testing.T) {
	// One line longer than the scanner buffer must fail instead of growing the
	// buffer without bound.
	line := "data: " + strings.Repeat("x", sseMaxLineBytes+1) + "\n\n"
	_, err := readAllFrames(t, line, 0)
	if err == nil {
		t.Fatal("an oversized line must fail")
	}
}

func TestSSEDecoderStopsAtDoneWithoutReadingFurther(t *testing.T) {
	frames, err := readAllFrames(t, "data: [DONE]\n\ndata: after\n\n", 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("frames = %+v, want none after [DONE]", frames)
	}
}
