package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/authz"
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
	case httputil.CodeSandboxPolicyDenied:
		// A resource asked for a work directory or a writable root the instance
		// forbids. That is a rejected request, not a server fault; keep the same
		// status httputil.WriteErr already gives this code everywhere else.
		status = http.StatusForbidden
	}
	httputil.WriteError(w, status, code)
}

// handleListResources is the management listing: it shows every state, drafts
// and pending records included, because a curator cannot act on what a listing
// hides. Consumers that only want invocable records ask for the discoverable
// view; the filtering happens here so no client has to re-implement the
// approval rule (and an unapproved record never leaves the server).
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
	discoverable := false
	if raw := q.Get("discoverable"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
			return
		}
		discoverable = parsed
	}
	limit, offset, ok := pageWindow(w, q)
	if !ok {
		return
	}
	filter := registry.Filter{
		Kind:      kind,
		Query:     q.Get("q"),
		Namespace: q.Get("namespace"),
		Status:    q.Get("status"),
	}
	entries := d.visibleEntries(r, d.Reg.ListFiltered(filter))
	if discoverable {
		// Discoverable is the single consumer-facing gate used by /api/workspace,
		// the MCP/Agent Card exports and A2A selection. Applying it here (rather
		// than filtering by status) also keeps legacy records whose status is
		// empty, which EffectiveStatus reads as approved.
		entries = registry.Discoverable(entries)
	}
	// The counts describe the catalogue, not the page: they drive the tabs, and a
	// tab that showed "0" because the caller was looking at page three of another
	// kind would be worse than no count at all. Same filters minus kind, one extra
	// pass over the already-loaded set (no extra query when kind is empty and the
	// discoverable view was not asked for).
	countSet := entries
	if kind != "" || discoverable {
		unfiltered := filter
		unfiltered.Kind = ""
		countSet = d.visibleEntries(r, d.Reg.ListFiltered(unfiltered))
		if discoverable {
			countSet = registry.Discoverable(countSet)
		}
	}
	byKind, byHealth := countsOf(countSet)
	total := len(entries)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"resources": pageOf(entries, limit, offset),
		"counts":    byKind,
		"health":    byHealth,
		// total is the size of the whole filtered, visibility-filtered set, so the
		// console can render "1–25 of 137" and know when to stop asking.
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// maxResourcePage bounds a page. Without a bound "pagination" would be a way to
// ask for the whole catalogue with extra steps; 200 is roughly what the console
// renders before scrolling stops being useful.
const maxResourcePage = 200

// pageWindow parses limit/offset. limit 0 (the default) means "everything", which
// is what every caller before paging existed asked for, so an old client keeps
// working unchanged.
func pageWindow(w http.ResponseWriter, q url.Values) (limit, offset int, ok bool) {
	limit, err := intQuery(q, "limit")
	if err != nil || limit < 0 || limit > maxResourcePage {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return 0, 0, false
	}
	offset, err = intQuery(q, "offset")
	if err != nil || offset < 0 {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return 0, 0, false
	}
	return limit, offset, true
}

// intQuery reads a non-negative integer parameter; empty means zero.
func intQuery(q url.Values, key string) (int, error) {
	raw := strings.TrimSpace(q.Get(key))
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}

// pageOf slices the already-filtered set. Both bounds are clamped: an offset past
// the end is an empty page (not an error — the console's own total says so), and
// the result is never nil so the wire carries [] rather than null.
func pageOf(entries []*registry.Entry, limit, offset int) []*registry.Entry {
	if offset >= len(entries) {
		return []*registry.Entry{}
	}
	if offset > 0 {
		entries = entries[offset:]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		return []*registry.Entry{}
	}
	return entries
}

// maxBatchIDs caps a batch read. It is the same order as the page ceiling: a
// batch is a way to ask for the records a page referenced, not for the catalogue.
const maxBatchIDs = 100

// handleBatchGetResources answers "give me these records" in one round trip.
//
// It exists because paging changed what a client can know: a grant row or a task
// references a resource id that may be on page seven, and resolving those one
// GET at a time is a burst of requests for something that is a single query on
// this side. Absent and not-visible ids both land in "missing", the same way a
// GET of somebody else's record is a 404 rather than a 403.
func (d Deps) handleBatchGetResources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := httputil.Decode(w, r, &req, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	ids, ok := normalizeBatchIDs(req.IDs)
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	found := d.Reg.GetMany(ids)
	visible := d.visibleEntries(r, entryList(found, ids))
	byID := make(map[string]*registry.Entry, len(visible))
	for _, e := range visible {
		byID[e.ID] = e
	}
	resources := make([]*registry.Entry, 0, len(ids))
	missing := make([]string, 0)
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			resources = append(resources, e)
			continue
		}
		missing = append(missing, id)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"resources": resources,
		"missing":   missing,
	})
}

// normalizeBatchIDs rejects a request that cannot be answered meaningfully and
// removes repeats, keeping the order of first appearance so the response can be
// zipped back onto the request.
func normalizeBatchIDs(raw []string) ([]string, bool) {
	if len(raw) == 0 || len(raw) > maxBatchIDs {
		return nil, false
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, false
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, true
}

// entryList projects the batch result onto the request order that is still known
// at this point: it keeps visibleEntries' contract (a slice) without letting a map
// iteration decide what the caller's page order looks like.
func entryList(found map[string]*registry.Entry, ids []string) []*registry.Entry {
	out := make([]*registry.Entry, 0, len(found))
	for _, id := range ids {
		if e, ok := found[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

func (d Deps) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	var entry registry.Entry
	if err := httputil.Decode(w, r, &entry, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if err := normalizeVisibility(&entry); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, registry.CodeInvalidVisibility)
		return
	}
	stored, err := d.Reg.Register(&entry)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	// Permissions answer "may this caller use POST /api/resources"; resource
	// grants answer "may this caller touch this particular record". A key with
	// every scope is still not the owner, and CanAccessResource treats a record
	// with no grants as closed — so without this row the creator's own PUT,
	// probe, curation and DELETE would all answer 404 not_found.
	if err := d.grantResourceCreator(r, stored.ID); err != nil {
		_ = d.Reg.Delete(stored.ID)
		if d.Log != nil {
			d.Log.Error("handler: creator grant failed", "id", stored.ID, "error", err)
		}
		httputil.WriteErr(w, err)
		return
	}
	d.Log.Info("handler: resource registered", "id", stored.ID, "kind", stored.Kind)
	httputil.WriteJSON(w, http.StatusCreated, stored)
}

// grantResourceCreator records the caller's ownership of a record it just
// registered. Owners bypass grants, anonymous callers have no principal, and an
// instance without an authz store has nowhere to write — all three keep the
// pre-identity behaviour unchanged.
func (d Deps) grantResourceCreator(r *http.Request, resourceID string) error {
	if d.Authz == nil || d.Authz.Store() == nil {
		return nil
	}
	p := PrincipalFrom(r)
	if p == nil || p.IsOwner || p.UserID == "" {
		return nil
	}
	return d.Authz.Store().CreateGrant(r.Context(), &authz.Grant{
		ResourceID:    resourceID,
		PrincipalKind: authz.PrincipalUser,
		PrincipalID:   p.UserID,
		Level:         authz.LevelWrite,
	})
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
	if err := normalizeVisibility(&entry); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, registry.CodeInvalidVisibility)
		return
	}
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

// handleResourceProbe asks the resource's own connector how the resource is
// doing and stores the answer. Unlike the health endpoint, which records an
// operator's judgement, this one is a measurement: an agent that publishes an
// Agent Card is fetched, and a card that cannot be fetched means the agent is not
// there however healthy the record claims to be.
func (d Deps) handleResourceProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, ok := d.Reg.Get(id)
	if !ok || !d.canAccess(r, id, true) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	conn, err := adapter.For(entry, d.adapterOptions())
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	health, err := conn.Health(r.Context(), entry)
	// A probe that could not be answered is itself a measurement: the resource
	// did not respond, so the record moves to unhealthy instead of keeping
	// whatever it said before the probe.
	reason := ""
	if err != nil {
		health = registry.HealthUnhealthy
		reason = string(httputil.AsError(err).Code)
	}
	if err := d.Reg.SetHealth(id, health); err != nil {
		writeDomainErr(w, err)
		return
	}
	payload := map[string]any{"id": id, "health": string(health), "probed": true}
	if reason != "" {
		payload["reason"] = reason
	}
	httputil.WriteJSON(w, http.StatusOK, payload)
}

// adapterOptions mirrors the options the hub invokes with, so a probe measures
// the same limits a real call will be subject to.
func (d Deps) adapterOptions() adapter.Options {
	opts := adapter.Options{}
	if d.Cfg != nil {
		opts.CLITimeout = d.Cfg.CLITimeout
		opts.CLIAllowlist = d.Cfg.CLIAllowlist
		opts.MaxOutputBytes = d.Cfg.MaxOutputBytes
		opts.ModelRouterMode = d.Cfg.ModelRouterMode
		opts.AgentTimeout = d.Cfg.AgentTimeout
	}
	return opts
}

func (d Deps) handleListTasks(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"tasks": d.Hub.Store().List(r.Context())})
}

func (d Deps) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task := d.Hub.Store().Get(r.Context(), r.PathValue("id"))
	if task == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, task)
}

func (d Deps) handleTaskSpans(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d.Hub.Store().Get(r.Context(), id) == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"spans": d.Hub.Store().Spans(r.Context(), id)})
}

// handleTaskEventStream exposes the broker feed for one task.
func (d Deps) handleTaskEventStream(w http.ResponseWriter, r *http.Request) {
	d.streamTaskEvents(w, r)
}

// handleClearTasks deletes the caller's finished task records: the console's
// "clear all". Runs that are still live are kept and counted in the answer, so
// the screen can say why rows remain instead of pretending the list is empty.
func (d Deps) handleClearTasks(w http.ResponseWriter, r *http.Request) {
	deleted, kept, err := d.Hub.Store().ClearTerminal(r.Context())
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "kept": kept})
}

func (d Deps) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	task, err := d.Hub.Cancel(r.Context(), r.PathValue("id"))
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
	task := d.Hub.Store().Get(r.Context(), id)
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
	replayed := d.Hub.Events(r.Context(), id, since, 0)
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
		for _, span := range d.Hub.Store().Spans(r.Context(), id) {
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

// normalizeVisibility validates the publication state a client sent.
//
// An empty value is accepted and left empty: the store and the domain both read
// empty as restricted, so an update that omits the field cannot silently
// publish a record. Anything else must be one of the two known states — a typo
// here decides who can invoke a resource, which is not a place for a guess.
func normalizeVisibility(entry *registry.Entry) error {
	if entry.Visibility == "" {
		return nil
	}
	parsed, err := registry.ParseVisibility(string(entry.Visibility))
	if err != nil {
		return err
	}
	entry.Visibility = parsed
	return nil
}
