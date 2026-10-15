package handlers

import (
	"errors"
	"net/http"
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
		registry.CodeInvalidEntry, registry.CodeDuplicate:
		status = http.StatusBadRequest
	case registry.CodeNotFound, hub.CodeTaskNotFound:
		status = http.StatusNotFound
	case hub.CodeInvalidStateTransition, hub.CodeTaskNotResumable, hub.CodeNoResource, hub.CodeInvalidFlow:
		status = http.StatusBadRequest
	}
	httputil.WriteError(w, status, code)
}

func (d Deps) handleListResources(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "" {
		if _, err := registry.ParseKind(kind); err != nil {
			writeDomainErr(w, err)
			return
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"resources": d.Reg.List(kind, r.URL.Query().Get("q")),
		"counts":    d.Reg.CountsByKind(),
		"health":    d.Reg.HealthCounts(),
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
	entry, ok := d.Reg.Get(r.PathValue("id"))
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, entry)
}

func (d Deps) handleUpdateResource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := d.Reg.Get(id); !ok {
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
	if err := d.Reg.Delete(r.PathValue("id")); err != nil {
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
	if err := d.Reg.SetHealth(r.PathValue("id"), health); err != nil {
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

func (d Deps) handleExportMCP(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"servers": registry.MCPServerDescriptions(d.Reg)})
}

func (d Deps) handleExportAgentCard(w http.ResponseWriter, r *http.Request) {
	card := registry.AgentCard(d.Reg, baseURL(r), config.Version, config.ProtocolVersion)
	httputil.WriteJSON(w, http.StatusOK, card)
}

// streamTaskEvents replays what already happened for a task and then follows
// the live broker feed until the task reaches a terminal state.
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
			var err error
			switch ev.Kind {
			case "status":
				err = httputil.WriteSSE(w, flusher, a2a.EventTaskStatus, ev.Status)
			case "artifact":
				err = httputil.WriteSSE(w, flusher, a2a.EventTaskArtifact, ev.Artifact)
			case "message":
				err = httputil.WriteSSE(w, flusher, a2a.EventTaskMessage, ev.Message)
			case "span":
				err = httputil.WriteSSE(w, flusher, a2a.EventTaskSpan, ev.Span)
			default:
				err = httputil.WriteSSE(w, flusher, ev.Kind, ev)
			}
			if err != nil {
				return
			}
			if ev.Kind == "status" && ev.Status != nil && (ev.Status.Status.State.IsTerminal() || ev.Status.Status.State == a2a.TaskStateInputRequired) {
				return
			}
		}
	}
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
