package handlers

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/auth"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/threads"
)

// handleThreadAudio accepts a recorded voice clip, transcribes it server side, drops
// the transcript into the thread as a voice-originated message, delegates it to the
// thread's participants and attaches the agent's reply as a TTS audio artifact.
//
// agenthub has no global body-size middleware and no multipart support elsewhere, so
// this endpoint builds its own bounded reader and multipart parser.
func (d Deps) handleThreadAudio(w http.ResponseWriter, r *http.Request) {
	if d.Threads == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeSpeechDisabled)
		return
	}
	// Only the offline mock ships in the first release; a real provider is a future
	// switch that reuses the same path.
	if d.Cfg.SpeechMode != registry.ProviderModeMock {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeSpeechDisabled)
		return
	}
	id := r.PathValue("id")
	th, err := d.Threads.GetThread(r.Context(), id, "")
	if err != nil || th == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeChannelNotFound)
		return
	}

	ct := r.Header.Get("Content-Type")
	if ct == "" || !isMultipart(ct) {
		httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.CodeAudioUnsupported)
		return
	}
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.CodeAudioUnsupported)
		return
	}
	boundary := params["boundary"]
	if boundary == "" {
		httputil.WriteError(w, http.StatusUnsupportedMediaType, httputil.CodeAudioUnsupported)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, int64(d.Cfg.AudioMaxBytes))
	mr := multipart.NewReader(r.Body, boundary)
	audio, err := readFirstFile(mr, d.Cfg.AudioMaxBytes)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeAudioUnsupported)
		return
	}
	if len(audio) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeAudioUnsupported)
		return
	}

	transcript, err := adapter.DefaultSpeech.Transcribe(r.Context(), audio)
	if err != nil {
		httputil.WriteError(w, http.StatusUnprocessableEntity, httputil.CodeAudioTranscribeFailed)
		return
	}

	if _, err := d.Threads.AddMessage(r.Context(), &threads.Message{ThreadID: id, Body: transcript, Origin: "voice"}); err != nil {
		httputil.WriteErr(w, err)
		return
	}

	mentions := mentionsOf(transcript)
	var task *a2a.Task
	for _, p := range th.Participants {
		if len(mentions) > 0 && !mentions[p] {
			continue
		}
		entry, ok := d.Reg.Get(p)
		if !ok {
			continue
		}
		_ = entry
		params := a2a.MessageSendParams{Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(transcript))}
		params.Message.Metadata = map[string]any{"resourceId": p, "threadId": id, "voice": true}
		t, err := d.Hub.Send(r.Context(), params)
		if err != nil {
			continue
		}
		task = t
	}

	out := map[string]any{"transcript": transcript}
	if task != nil {
		out["taskId"] = task.ID
		// Attach the agent's reply as a TTS audio artifact.
		if reply := lastAgentText(task); reply != "" {
			if audioOut, err := adapter.DefaultSpeech.Synthesize(r.Context(), reply); err == nil {
				name := "reply.wav"
				mime := "audio/wav"
				encoded := base64.StdEncoding.EncodeToString(audioOut)
				artifact := a2a.Artifact{
					ArtifactID: auth.NewID("art"),
					Name:       "reply.wav",
					Parts: []a2a.Part{{
						Type: a2a.PartTypeFile,
						File: &a2a.FileContent{
							Name:     &name,
							MimeType: &mime,
							Bytes:    &encoded,
						},
					}},
				}
				if d.Hub.Store() != nil {
					if _, err := d.Hub.Store().AddArtifact(task.ID, artifact); err != nil {
						httputil.WriteErr(w, err)
						return
					}
				}
			}
		}
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

func isMultipart(ct string) bool {
	return len(ct) >= 19 && ct[:19] == "multipart/form-data"
}

// readFirstFile reads at most max bytes from the first file part of a multipart
// stream, ignoring the rest. Returns an error when no file part is present.
func readFirstFile(mr *multipart.Reader, max int) ([]byte, error) {
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		defer part.Close()
		limited := io.LimitReader(part, int64(max)+1)
		data, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if len(data) > max {
			return nil, io.ErrUnexpectedEOF
		}
		return data, nil
	}
}

// lastAgentText returns the text of the most recent agent message in a task history.
func lastAgentText(task *a2a.Task) string {
	for i := len(task.History) - 1; i >= 0; i-- {
		if task.History[i].Role == a2a.RoleAgent {
			return task.History[i].Text()
		}
	}
	return ""
}
