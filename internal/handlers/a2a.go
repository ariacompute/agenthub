package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"agenthub/internal/a2a"
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
		a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeMethodNotFound, a2a.MsgMethodNotFound))
		return
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
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeResourceNotFound, a2a.MsgResourceNotFound))
			return
		}
		d.streamTask(w, r, req.ID, params)

	case a2a.MethodTasksGet:
		var params a2a.TaskQueryParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		task, err := d.Hub.Get(params.ID, params.HistoryLength)
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
		task, err := d.Hub.Cancel(params.ID)
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
		cfg, err := d.Hub.SetPush(params)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, cfg)

	case a2a.MethodPushGet:
		var params a2a.TaskQueryParams
		if err := decodeParams(req.Params, &params); err != nil {
			a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeInvalidParams, a2a.MsgInvalidParams))
			return
		}
		cfg, err := d.Hub.GetPush(params.ID)
		if err != nil {
			a2a.WriteRPCErr(w, req.ID, err)
			return
		}
		a2a.WriteResult(w, req.ID, cfg)

	default:
		a2a.WriteRPCError(w, req.ID, a2a.NewError(a2a.CodeUnsupportedOperation, a2a.MsgUnsupportedOperation))
	}
}

// mayInvoke decides whether the caller may run this resource.
//
// The A2A data plane stays reachable without a session — a peer agent must be
// able to send its first task — but a resource that carries grants is only
// invocable by the users and groups it was granted to. An explicitly named
// Only an explicitly named resource is checked: a request that names nothing
// leaves the choice to the hub's kind fallback, and the id it lands on is not
// known before the run starts. Restricting a resource is therefore only
// meaningful together with listing it, which is where the same grant hides it.
func (d Deps) mayInvoke(r *http.Request, params *a2a.MessageSendParams) bool {
	if params.Metadata == nil {
		return true
	}
	raw, ok := params.Metadata["resourceId"]
	if !ok {
		return true
	}
	id, ok := raw.(string)
	if !ok || id == "" {
		return true
	}
	return d.canAccess(r, id, false)
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
