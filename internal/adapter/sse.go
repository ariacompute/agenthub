package adapter

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// errStreamTooLarge separates "the upstream streamed more than we were willing
// to read" from a malformed frame: the first is a resource limit, the second a
// protocol violation. Callers map both to a machine readable adapter code.
var errStreamTooLarge = errors.New("adapter: streamed response exceeds the limit")

const (
	// sseMaxLineBytes caps one line. A gateway that never emits a newline must
	// not be able to grow the scanner's buffer without bound.
	sseMaxLineBytes = 64 << 10
	// sseDefaultLimit caps the whole stream.
	sseDefaultLimit = 1 << 20
)

// sseDecoder reads Server-Sent Events the way every OpenAI compatible gateway
// and A2A streaming endpoint writes them: "event:" and "data:" fields, blank
// line separated, comments (":") as heartbeats, "[DONE]" as the terminal
// sentinel. It is shared by the model and agent connectors so the two never
// drift apart.
type sseDecoder struct {
	sc    *bufio.Scanner
	limit int
	total int
	event string
	data  []string
}

func newSSEDecoder(r io.Reader) *sseDecoder {
	return newSSEDecoderLimit(r, sseDefaultLimit)
}

func newSSEDecoderLimit(r io.Reader, limit int) *sseDecoder {
	if limit <= 0 {
		limit = sseDefaultLimit
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), sseMaxLineBytes)
	return &sseDecoder{sc: sc, limit: limit}
}

// Next returns one event. ok is false when the stream ended, either because the
// upstream sent [DONE] or because it closed cleanly. An incomplete frame at EOF
// is still delivered: some servers omit the trailing blank line.
func (d *sseDecoder) Next() (event, data string, ok bool, err error) {
	for d.sc.Scan() {
		line := d.sc.Text()
		d.total += len(line) + 1
		if d.total > d.limit {
			return "", "", false, errStreamTooLarge
		}
		if line == "" {
			frame, hasData := d.flush()
			if !hasData {
				continue
			}
			return frame.event, frame.data, true, nil
		}
		if strings.HasPrefix(line, ":") {
			// Comment or keep-alive; it carries no payload.
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			d.event = value
		case "data":
			if strings.TrimSpace(value) == "[DONE]" {
				return "", "", false, nil
			}
			d.data = append(d.data, value)
		}
	}
	if err := d.sc.Err(); err != nil {
		return "", "", false, err
	}
	frame, hasData := d.flush()
	if !hasData {
		return "", "", false, nil
	}
	return frame.event, frame.data, true, nil
}

type sseFrame struct {
	event string
	data  string
}

func (d *sseDecoder) flush() (sseFrame, bool) {
	if len(d.data) == 0 && d.event == "" {
		return sseFrame{}, false
	}
	event := d.event
	if event == "" {
		event = "message"
	}
	frame := sseFrame{event: event, data: strings.Join(d.data, "\n")}
	d.event, d.data = "", nil
	return frame, true
}
