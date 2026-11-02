package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"agenthub/internal/a2a"
	"agenthub/internal/audit"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// errMissingParams marks a request with no params object.
var errMissingParams = errors.New("missing_params")

// handleAgentCard serves A2A discovery at the well-known path. Discovery stays
// open to anonymous peers, but it advertises only the resources the caller may
// read: a resource granted to one group is not published to the world.
func (d Deps) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	entries := d.visibleEntries(r, d.Reg.Snapshot())
	byKind, _ := countsOf(entries)
	card := registry.AgentCardOf(entries, byKind, len(entries), baseURL(r), config.Version, config.ProtocolVersion)
	httputil.WriteJSON(w, http.StatusOK, card)
}

// handleJSONRPC is the single A2A entry point.
func (d Deps) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	var req a2a.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		a2a.WriteRPCError(w, nil, a2a.NewError(a2a.CodeParseError, a2a.MsgParseError))
		return
	}
	if req.JSONRPC != a2a.JSONRPCVersion {
		a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidRequest, a2a.MsgInvalidRequest))
		return
	}
	method, ok := a2a.Canonical(req.Method, d.Cfg.LegacyMethods)
	if !ok {
		if tr := audit.FromContext(r.Context()); tr != nil {
			tr.NoteAction(req.Method)
			tr.NoteError(a2a.MsgMethodNotFound)
		}
		a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeMethodNotFound, a2a.MsgMethodNotFound))
		return
	}
	// The audit action of a data plane call is the A2A method, not the HTTP
	// path: every call arrives as POST /a2a, so the path alone says nothing
	// about what was asked.
	if tr := audit.FromContext(r.Context()); tr != nil {
		tr.NoteAction(method)
		tr.NoteReadOnly(method == a2a.MethodTasksGet || method == a2a.MethodPushGet)
	}

	switch method {
	case a2a.MethodMessageSend:
		var params a2a.MessageSendParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.mayInvoke(r, &params) {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound))
			return
		}
		task, err := d.Hub.Send(r.Context(), params)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, task)

	case a2a.MethodMessageStream:
		var params a2a.MessageSendParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.mayInvoke(r, &params) {
			streamRPCError(w, req.ID, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound))
			return
		}
		d.streamTask(w, r, req.ID, params)

	case a2a.MethodTasksGet:
		var params a2a.TaskQueryParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.requireCaller(w, req.ID, r) {
			return
		}
		task, err := d.Hub.Get(r.Context(), params.ResolveTaskID(), params.HistoryLength)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, task)

	case a2a.MethodTasksCancel:
		var params a2a.TaskQueryParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.requireCaller(w, req.ID, r) {
			return
		}
		task, err := d.Hub.Cancel(r.Context(), params.ResolveTaskID())
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, task)

	case a2a.MethodTasksResume:
		var params a2a.TaskResumeParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.requireCaller(w, req.ID, r) {
			return
		}
		params.ID = params.ResolveTaskID()
		task, err := d.Hub.Resume(r.Context(), params)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, task)

	case a2a.MethodPushSet:
		var params a2a.TaskPushNotificationConfigParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.requireCaller(w, req.ID, r) {
			return
		}
		cfg, err := d.Hub.SetPush(r.Context(), params)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, cfg)

	case a2a.MethodPushGet:
		// Same params shape as pushNotification/set, so `id` and `taskId` are
		// both accepted here too (evaluation 6.4).
		var params a2a.TaskPushNotificationConfigParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		if !d.requireCaller(w, req.ID, r) {
			return
		}
		cfg, err := d.Hub.GetPush(r.Context(), params.ResolveTaskID())
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, cfg)

	default:
		a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeUnsupportedOperation, a2a.MsgUnsupportedOperation))
	}
}

// mayInvoke decides whether the caller may run the resource this message will
// reach.
//
// The A2A data plane stays reachable without a session — a peer agent must be
// able to send its first task — but only a published record is invocable by an
// anonymous peer, and a restricted one needs a grant. The resource is always
// resolved, even when the request names none: the hub's kind fallback used to
// pick an entry the caller never selected, which is how an unnamed request could
// land on somebody else's private agent. Resolving first and refusing when
// nothing resolves closes that path.
func (d Deps) mayInvoke(r *http.Request, params *a2a.MessageSendParams) bool {
	id := resourceIDOf(params.Metadata)
	kind := ""
	if id == "" {
		entry, err := d.Hub.ResolveResource(*params)
		if err != nil || entry == nil {
			noteRefusal(r, a2a.MsgResourceNotFound)
			return false
		}
		id, kind = entry.ID, string(entry.Kind)
	} else if d.Reg != nil {
		if entry, ok := d.Reg.Get(id); ok {
			kind = string(entry.Kind)
		}
	}
	if tr := audit.FromContext(r.Context()); tr != nil {
		tr.NoteResource(id, kind)
	}
	if !d.canAccess(r, id, false) {
		noteRefusal(r, "forbidden")
		return false
	}
	return true
}

// noteRefusal marks the audit event as a refusal, so the console can separate
// "somebody tried and was turned away" from "the call failed".
func noteRefusal(r *http.Request, code string) {
	if tr := audit.FromContext(r.Context()); tr != nil {
		tr.NoteError(code)
	}
}

// requireCaller rejects an anonymous caller for a method that acts on one
// existing task. message/send stays open; anything that reads, cancels, resumes
// or repoints a task belongs to somebody.
func (d Deps) requireCaller(w http.ResponseWriter, id any, r *http.Request) bool {
	if PrincipalFrom(r) != nil {
		return true
	}
	a2a.WriteRPCError(w, id, a2a.NewError(a2a.CodeUnauthenticated, a2a.MsgUnauthenticated))
	return false
}

// resourceIDOf reads the resource selector out of message metadata, accepting
// the same three spellings the hub resolves.
func resourceIDOf(metadata map[string]any) string {
	for _, key := range []string{"resourceId", "resource_id", "resource"} {
		if v, ok := metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// streamTask answers message/stream with an SSE body where every frame is a
// JSON-RPC response carrying one A2A event. There is no sentinel frame: the run
// ends with the terminal (or input-required) status update.
func (d Deps) streamTask(w http.ResponseWriter, r *http.Request, id any, params a2a.MessageSendParams) {
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}
	emit := func(event string, payload any) error {
		return httputil.WriteSSE(w, flusher, event, a2a.NewResponse(id, payload))
	}
	_, err := d.Hub.Stream(r.Context(), params, emit)
	if err != nil {
		rpcErr, isRPC := err.(*a2a.Error)
		if !isRPC {
			rpcErr = a2a.NewError(a2a.CodeInternalError, a2a.MsgInternalError)
		}
		if writeErr := httputil.WriteSSE(w, flusher, "error", a2a.NewErrorResponse(id, rpcErr)); writeErr != nil {
			slog.Debug("handlers: write stream error frame", "error", writeErr)
		}
	}
}

// streamRPCError answers a streaming request that is refused before the run
// starts — an unauthorized resource, for instance. It writes the same `error`
// frame streamTask writes for a failure mid-run, so a client only has to
// understand one shape no matter when the refusal happened.
func streamRPCError(w http.ResponseWriter, id any, rpcErr *a2a.Error) {
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}
	if err := httputil.WriteSSE(w, flusher, "error", a2a.NewErrorResponse(id, rpcErr)); err != nil {
		slog.Debug("handlers: write stream error frame", "error", err)
	}
}

// handleA2AStream is a console helper: it replays the broker events of an
// existing task as plain SSE frames (no JSON-RPC envelope).
func (d Deps) handleA2AStream(w http.ResponseWriter, r *http.Request) {
	d.streamTaskEvents(w, r)
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return errMissingParams
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}
