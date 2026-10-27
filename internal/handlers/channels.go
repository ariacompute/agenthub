package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/auth"
	"agenthub/internal/channels"
	"agenthub/internal/httputil"
	"agenthub/internal/threads"
)

// mentionsOf returns the set of @mention resource ids referenced in a message, or nil
// when there are none (meaning: address every participant).
func mentionsOf(text string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.Fields(text) {
		if strings.HasPrefix(tok, "@") {
			out[strings.TrimPrefix(tok, "@")] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// channelsReady short-circuits every channel endpoint when the feature is not wired
// (no store or disabled), returning a non-404 so the listener test stays green.
func (d Deps) channelsReady(w http.ResponseWriter) bool {
	if !d.Cfg.ChannelsEnabled || d.Channels == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeChannelDisabled)
		return false
	}
	return true
}

type channelRequest struct {
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	Config        map[string]any `json:"config"`
	SigningKeyEnv string         `json:"signingKeyEnv"`
	ThreadID      string         `json:"threadId"`
}

func (d Deps) handleListChannels(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	list, err := d.Channels.List(r.Context(), PrincipalFrom(r).UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, list)
}

func (d Deps) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	c, err := d.Channels.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeChannelNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, c)
}

func (d Deps) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	var req channelRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	kind := channels.NormalizeKind(req.Kind)
	if kind == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeChannelPayloadInvalid)
		return
	}
	if !validEnvVarName(req.SigningKeyEnv) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeChannelPayloadInvalid)
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	c := &channels.Channel{
		ID:            auth.NewID("chan"),
		Kind:          kind,
		Name:          req.Name,
		Enabled:       req.Enabled,
		Config:        req.Config,
		SigningKeyEnv: req.SigningKeyEnv,
		ThreadID:      req.ThreadID,
		OwnerID:       PrincipalFrom(r).UserID,
	}
	created, err := d.Channels.Create(r.Context(), c)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}

func (d Deps) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	c, err := d.Channels.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeChannelNotFound)
		return
	}
	var req channelRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.Name != "" {
		c.Name = req.Name
	}
	c.Enabled = req.Enabled
	if req.Config != nil {
		c.Config = req.Config
	}
	if req.SigningKeyEnv != "" && validEnvVarName(req.SigningKeyEnv) {
		c.SigningKeyEnv = req.SigningKeyEnv
	}
	if req.ThreadID != "" {
		c.ThreadID = req.ThreadID
	}
	updated, err := d.Channels.Update(r.Context(), c)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, updated)
}

func (d Deps) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	if err := d.Channels.Delete(r.Context(), r.PathValue("id")); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	list, err := d.Channels.ListDeliveries(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, list)
}

// handleChannelWebhook is the public inbound endpoint. The platform signs the raw
// body; agenthub verifies it against the secret held in the environment variable
// named by signing_key_env (never persisted), then dedupes by external_ref and
// forwards the message into the bound thread.
func (d Deps) handleChannelWebhook(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	channelID := r.PathValue("id")
	c, err := d.Channels.Get(r.Context(), channelID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeChannelNotFound)
		return
	}
	if !c.Enabled {
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeChannelDisabled)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := channels.Verify(c.Kind, body, r, c.SigningKeyEnv); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	inbound, externalRef := parseWebhookPayload(c.Kind, body, r)
	if externalRef == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeChannelPayloadInvalid)
		return
	}
	msg := &channels.ChannelMessage{
		ChannelID:   channelID,
		ExternalRef: externalRef,
		ThreadID:    c.ThreadID,
		Direction:   "inbound",
		Payload:     inbound,
		Status:      channels.StatusReceived,
	}
	inserted, err := d.Channels.RecordDelivery(r.Context(), msg)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if !inserted {
		// Duplicate delivery: idempotent no-op.
		w.WriteHeader(http.StatusOK)
		return
	}
	// Forward into the bound thread when one is configured.
	if c.ThreadID != "" && d.Threads != nil {
		if text, ok := inbound["text"].(string); ok && text != "" {
			if _, err := d.Threads.AddMessage(r.Context(), &threads.Message{ThreadID: c.ThreadID, Body: text, Origin: string(c.Kind)}); err == nil {
				d.delegateThread(r, c.ThreadID, text)
			}
		}
	}
	httputil.WriteJSON(w, http.StatusOK, msg)
}

// handleDeliverChannel sends a message out through a channel's configured target
// URL — the inverse of the inbound webhook. It returns the provider's raw
// response verbatim (status + <=512B body slice) and never retries or buffers.
func (d Deps) handleDeliverChannel(w http.ResponseWriter, r *http.Request) {
	if !d.channelsReady(w) {
		return
	}
	channelID := r.PathValue("id")
	c, err := d.Channels.Get(r.Context(), channelID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeChannelNotFound)
		return
	}
	if !c.Enabled {
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeChannelDisabled)
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.Message == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeChannelPayloadInvalid)
		return
	}
	body, _ := json.Marshal(map[string]any{"text": req.Message})
	result, err := channels.Deliver(r.Context(), c, string(body))
	if err != nil {
		httputil.WriteError(w, http.StatusBadGateway, httputil.CodeDeliveryFailed)
		return
	}
	_, _ = d.Channels.RecordDelivery(r.Context(), &channels.ChannelMessage{
		ChannelID:   channelID,
		ExternalRef: "out-" + auth.NewID("dlv"),
		ThreadID:    c.ThreadID,
		Direction:   "outbound",
		Payload:     map[string]any{"status": result.Status, "raw": result.Raw},
		Status:      channels.StatusDelivered,
	})
	httputil.WriteJSON(w, http.StatusOK, result)
}

// validEnvVarName rejects anything that is not a plausible, safe environment
// variable name. We only ever store the name, never the value.
func validEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// parseWebhookPayload extracts the human-facing text and an idempotency key from a
// platform payload. The shapes differ per provider; for the first release we accept
// a small, provider-tagged envelope so the console can route any of them.
func parseWebhookPayload(kind channels.Kind, body []byte, r *http.Request) (map[string]any, string) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, ""
	}
	text, externalRef := "", ""
	switch kind {
	case channels.KindTelegram:
		if msg, ok := raw["message"].(map[string]any); ok {
			if t, ok := msg["text"].(string); ok {
				text = t
			}
			if mid, ok := msg["message_id"].(float64); ok {
				externalRef = "tg-" + itoa(int(mid))
			}
		}
	case channels.KindSlack:
		if t, ok := raw["text"].(string); ok {
			text = t
		}
		if e, ok := raw["event_id"].(string); ok {
			externalRef = "sl-" + e
		}
	default:
		if t, ok := raw["text"].(string); ok {
			text = t
		}
		if e, ok := raw["id"].(string); ok {
			externalRef = e
		}
	}
	raw["text"] = text
	return raw, externalRef
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// delegateThread forwards a channel message to every participant of a thread, mirroring
// handlePostThreadMessage's mention delegation.
func (d Deps) delegateThread(r *http.Request, threadID, text string) {
	th, err := d.Threads.GetThread(r.Context(), threadID, "")
	if err != nil || th == nil {
		return
	}
	mentions := mentionsOf(text)
	for _, p := range th.Participants {
		if len(mentions) > 0 && !mentions[p] {
			continue
		}
		if _, ok := d.Reg.Get(p); !ok {
			continue
		}
		params := a2a.MessageSendParams{Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(text))}
		params.Message.Metadata = map[string]any{"resourceId": p, "threadId": threadID}
		if _, err := d.Hub.Send(r.Context(), params); err != nil {
			// Delegation failure must not change the channel message status; the
			// task list already reflects the failure.
			continue
		}
	}
}
