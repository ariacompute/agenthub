package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// writeDomainErr maps a registry / hub / A2A error onto an HTTP status while
// keeping the machine readable code on the wire. The console localises the
// code; the status only has to be honest about the failure class.
func writeDomainErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError

	var rpc *a2a.Error
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case a2a.CodeTaskNotFound, a2a.CodeResourceNotFound:
			status = http.StatusNotFound
		case a2a.CodeTaskNotCancelable, a2a.CodeTaskNotResumable,
			a2a.CodeInvalidStateTransition, a2a.CodePushNotificationNotSupported,
			a2a.CodeUnsupportedOperation:
			status = http.StatusBadRequest
		}
		httputil.WriteError(w, status, httputil.Code(rpc.Message))
		return
	}

	code := httputil.AsError(err).Code
	switch code {
	case registry.CodeInvalidKind, registry.CodeInvalidHealth, registry.CodeInvalidTransport,
		registry.CodeInvalidEntry, registry.CodeDuplicate, registry.CodeInvalidKindConfig,
		registry.CodeInvalidStatusTransition, registry.CodeInvalidNamespace,
		registry.CodeInvalidSource:
		status = http.StatusBadRequest
	case registry.CodeSyncInFlight:
		// The source is already being pulled: the caller should come back, not
		// change its request.
		status = http.StatusConflict
	case registry.CodeNotFound, hub.CodeTaskNotFound:
		status = http.StatusNotFound
	case hub.CodeInvalidStateTransition, hub.CodeTaskNotResumable, hub.CodeNoResource, hub.CodeInvalidFlow:
		status = http.StatusBadRequest
	case hub.CodeRestoring:
		// The hub refuses work while a restore rewrites the database: it is a
		// temporary condition, so the caller is told to come back, not to fix
		// its request.
		status = http.StatusServiceUnavailable
	}
	httputil.WriteError(w, status, code)
}

// handleListResources is the management listing: it shows every state, drafts
// and pending records included, because a curator cannot act on what a listing
// hides. The discoverable catalogue is the export endpoints, which filter down
// to approved records.
func (d Deps) handleListResources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" {
		if _, err := registry.ParseKind(kind); err != nil {
			writeDomainErr(w, err)
			return
		}
	}
	if st := q.Get("status"); st != "" {
		if _, err := registry.ParseStatus(st); err != nil {
			writeDomainErr(w, err)
			return
		}
	}
	entries := d.visibleEntries(r, d.Reg.ListFiltered(registry.Filter{
		Kind:      kind,
		Query:     q.Get("q"),
		Namespace: q.Get("namespace"),
		Status:    q.Get("status"),
	}))
	byKind, byHealth := countsOf(entries)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"resources": entries,
		"counts":    byKind,
		"health":    byHealth,
	})
}

func (d Deps) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	var entry registry.Entry
	if err := httputil.Decode(w, r, &entry, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	stored, err := d.Reg.Register(&entry)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	d.Log.Info("handler: resource registered", "id", stored.ID, "kind", stored.Kind)
	httputil.WriteJSON(w, http.StatusCreated, stored)
}

func (d Deps) handleGetResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, ok := d.Reg.Get(id)
	if !ok || !d.canAccess(r, id, false) {
		// A resource the caller may not see is reported as absent: revealing
		// that it exists would leak the shape of somebody else's catalogue.
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, entry)
}

func (d Deps) handleUpdateResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := d.Reg.Get(id); !ok || !d.canAccess(r, id, true) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var entry registry.Entry
	if err := httputil.Decode(w, r, &entry, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	entry.ID = id
	stored, err := d.Reg.Upsert(&entry)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, stored)
}

func (d Deps) handleDeleteResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := d.Reg.Get(id); !ok || !d.canAccess(r, id, true) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := d.Reg.Delete(id); err != nil {
		writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleResourceHealth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Health string `json:"health"`
	}
	if err := httputil.Decode(w, r, &body, 1<<10); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	health, err := registry.ParseHealth(body.Health)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	id := r.PathValue("id")
	if _, ok := d.Reg.Get(id); !ok || !d.canAccess(r, id, true) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := d.Reg.SetHealth(id, health); err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"health": string(health)})
}

func (d Deps) handleListTasks(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"tasks": d.Hub.Store().List()})
}

func (d Deps) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task := d.Hub.Store().Get(r.PathValue("id"))
	if task == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, task)
}

func (d Deps) handleTaskSpans(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d.Hub.Store().Get(id) == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"spans": d.Hub.Store().Spans(id)})
}

// handleTaskEventStream exposes the broker feed for one task.
func (d Deps) handleTaskEventStream(w http.ResponseWriter, r *http.Request) {
	d.streamTaskEvents(w, r)
}

func (d Deps) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	task, err := d.Hub.Cancel(r.PathValue("id"))
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, task)
}

func (d Deps) handleResumeTask(w http.ResponseWriter, r *http.Request) {
	var params a2a.TaskResumeParams
	if err := httputil.Decode(w, r, &params, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	params.ID = r.PathValue("id")
	task, err := d.Hub.Resume(r.Context(), params)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, task)
}

// handleExportMCP and handleExportAgentCard publish the catalogue, so they
// publish what the caller may see — no more. An exporter that ignored grants
// would be the easiest way around them.
func (d Deps) handleExportMCP(w http.ResponseWriter, r *http.Request) {
	entries := d.visibleEntries(r, d.Reg.Snapshot())
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"servers": registry.MCPServerDescriptionsOf(entries)})
}

func (d Deps) handleExportAgentCard(w http.ResponseWriter, r *http.Request) {
	entries := d.visibleEntries(r, d.Reg.Snapshot())
	byKind, _ := countsOf(entries)
	card := registry.AgentCardOf(entries, byKind, len(entries), baseURL(r), config.Version, config.ProtocolVersion)
	httputil.WriteJSON(w, http.StatusOK, card)
}

// streamTaskEvents replays what already happened for a task and then follows
// the live broker feed until the task reaches a terminal state.
//
// since=<seq> resumes a broken stream: every persisted frame newer than seq is
// replayed before the live feed. Without it (or without event persistence) the
// current state of the task is rebuilt from spans, artifacts and status, which
// is the behaviour the console relied on before the stream was persisted.
func (d Deps) streamTaskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("task")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		id = r.PathValue("id")
	}
	task := d.Hub.Store().Get(id)
	if task == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}

	since := sinceSeq(r.URL.Query().Get("since"))
	replayed := d.Hub.Events(id, since, 0)
	if len(replayed) > 0 {
		for i := range replayed {
			if err := writeEventFrame(w, flusher, &replayed[i]); err != nil {
				return
			}
		}
		if isTerminalEvent(&replayed[len(replayed)-1]) {
			return
		}
	} else if since == 0 {
		for _, span := range d.Hub.Store().Spans(id) {
			if err := httputil.WriteSSE(w, flusher, a2a.EventTaskSpan, span); err != nil {
				return
			}
		}
		for _, art := range task.Artifacts {
			frame := a2a.TaskArtifactUpdateEvent{Kind: a2a.KindArtifactUpdate, TaskID: task.ID, ContextID: task.ContextID, Artifact: art, LastChunk: art.LastChunk}
			if err := httputil.WriteSSE(w, flusher, a2a.EventTaskArtifact, frame); err != nil {
				return
			}
		}
		if err := httputil.WriteSSE(w, flusher, a2a.EventTaskStatus, statusFrame(task)); err != nil {
			return
		}
		if isFinished(task) {
			return
		}
	}

	ch, cancel := d.Hub.Broker().Subscribe(id, 64)
	defer cancel()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if err := httputil.Comment(w, flusher, "keep-alive"); err != nil {
				return
			}
		case ev, open := <-ch:
			if !open {
				return
			}
			if err := writeEventFrame(w, flusher, &ev); err != nil {
				return
			}
			if isTerminalEvent(&ev) {
				return
			}
		}
	}
}

// writeEventFrame renders one hub event as the SSE frame its kind maps to.
func writeEventFrame(w http.ResponseWriter, flusher http.Flusher, ev *hub.Event) error {
	switch ev.Kind {
	case hub.EventKindStatus:
		return httputil.WriteSSE(w, flusher, a2a.EventTaskStatus, ev.Status)
	case hub.EventKindArtifact:
		return httputil.WriteSSE(w, flusher, a2a.EventTaskArtifact, ev.Artifact)
	case hub.EventKindMessage:
		return httputil.WriteSSE(w, flusher, a2a.EventTaskMessage, ev.Message)
	case hub.EventKindSpan:
		return httputil.WriteSSE(w, flusher, a2a.EventTaskSpan, ev.Span)
	default:
		return httputil.WriteSSE(w, flusher, ev.Kind, ev)
	}
}

// isTerminalEvent reports whether the frame ends the run. A2A streaming has no
// sentinel frame: the terminal status update is the end of the stream.
func isTerminalEvent(ev *hub.Event) bool {
	if ev.Kind != hub.EventKindStatus || ev.Status == nil {
		return false
	}
	state := ev.Status.Status.State
	return state.IsTerminal() || state == a2a.TaskStateInputRequired
}

// sinceSeq parses the resume cursor. An absent or invalid value replays from
// the start, which is the documented cold-start behaviour.
func sinceSeq(raw string) uint64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func statusFrame(task *a2a.Task) a2a.TaskStatusUpdateEvent {
	return a2a.TaskStatusUpdateEvent{
		Kind:      a2a.KindStatusUpdate,
		TaskID:    task.ID,
		ContextID: task.ContextID,
		Status:    task.Status,
		Final:     isFinished(task),
	}
}

func isFinished(task *a2a.Task) bool {
	return task.Status.State.IsTerminal() || task.Status.State == a2a.TaskStateInputRequired
}
