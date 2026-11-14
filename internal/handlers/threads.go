package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
	"agenthub/internal/router"
	"agenthub/internal/threads"
)

type threadCreateBody struct {
	Title        string   `json:"title"`
	Participants []string `json:"participants"`
}

type threadMessageBody struct {
	Body     string   `json:"body"`
	Mentions []string `json:"mentions"`
}

// handleListThreads lists the threads the caller owns.
func (d Deps) handleListThreads(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	list, err := d.Threads.ListThreads(r.Context(), p.UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"threads": list})
}

// handleCreateThread creates a thread owned by the caller.
func (d Deps) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body threadCreateBody
	if err := httputil.Decode(w, r, &body, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	p := PrincipalFrom(r)
	t := &threads.Thread{Title: body.Title, OwnerID: p.UserID, Participants: body.Participants}
	created, err := d.Threads.CreateThread(r.Context(), t)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if len(body.Participants) > 0 {
		_ = d.Threads.SetParticipants(r.Context(), created.ID, body.Participants)
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}

// handleGetThread returns a thread and its full message history.
func (d Deps) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := PrincipalFrom(r)
	t, err := d.Threads.GetThread(r.Context(), id, p.UserID)
	if err != nil {
		threadError(w, err)
		return
	}
	msgs, err := d.Threads.ListMessages(r.Context(), id, 0)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	// A reply that landed while no stream was attached is persisted here, on the
	// read path, so the page still shows it after a reload or a reconnect.
	d.fillPendingReplies(r.Context(), id, msgs)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"thread": t, "messages": msgs})
}

// handleDeleteThread removes one of the caller's threads together with its
// message history (the FOREIGN KEY cascades). The store reports a missing id as
// threads.ErrNotFound, which the console reads as "already gone" — that is what
// makes a multi-select delete safe to retry.
func (d Deps) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	if d.Threads == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	p := PrincipalFrom(r)
	if err := d.Threads.DeleteThread(r.Context(), r.PathValue("id"), p.UserID); err != nil {
		threadError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClearThreadMessages empties one owned thread's conversation while
// keeping the thread, its participants and the tasks already started from it.
// A missing or not-owned thread is the same 404 as handleDeleteThread, so the
// endpoint cannot be used to probe for ids the caller is not allowed to see.
func (d Deps) handleClearThreadMessages(w http.ResponseWriter, r *http.Request) {
	if d.Threads == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	p := PrincipalFrom(r)
	if err := d.Threads.ClearMessages(r.Context(), r.PathValue("id"), p.UserID); err != nil {
		threadError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePostThreadMessage appends a human message and, for every @mention,
// delegates the message to that agent through the hub (message/send over A2A).
// Each delegation is recorded as an (initially empty) agent message keyed by the
// resulting task id; the live reply lands via handleThreadEventStream.
func (d Deps) handlePostThreadMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := PrincipalFrom(r)
	if _, err := d.Threads.GetThread(r.Context(), id, p.UserID); err != nil {
		threadError(w, err)
		return
	}
	var body threadMessageBody
	if err := httputil.Decode(w, r, &body, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.Code("empty_message"))
		return
	}

	userMsg := &threads.Message{
		ThreadID: id, AuthorType: "user", AuthorID: p.UserID,
		AuthorName: p.Name, Body: body.Body,
	}
	userMsg, err := d.Threads.AddMessage(r.Context(), userMsg)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}

	// The @handle a person typed is the authoritative statement of who should
	// answer. The console also sends resource ids, but its picker can lag behind
	// the registry or miss an agent entirely and then fall back to a default
	// worker; trusting that id is how "@google-adk hi" came back from an
	// unrelated offline mock with a digest instead of the agent's answer.
	// Resolve the handles here, against the catalogue this caller may actually
	// invoke, and only keep the ids the console sent when the body names nobody.
	handles := mentionHandles(body.Body)
	mentions := body.Mentions
	fromHandle := false
	reasons := map[string]string{}
	if len(handles) > 0 {
		resolved := d.resolveMentions(r, handles)
		reasons = resolved.Reasons
		if len(resolved.IDs) > 0 {
			mentions = resolved.IDs
			fromHandle = true
		} else if len(mentions) == 0 {
			// An address to an agent that is not registered must not be served
			// by a default worker: that run would fabricate a digest instead of
			// the content the person asked for. The message is still posted,
			// together with the reason so the console can say what to fix.
			httputil.WriteJSON(w, http.StatusCreated, map[string]any{
				"message":   userMsg,
				"delegated": []string{},
				"failed":    handles,
				"routed":    false,
				"reason":    primaryReason(reasons),
				"reasons":   reasons,
			})
			return
		}
	}

	// Ids sent by the console are not authoritative either. A plain message (no
	// @handle) still has to pass the same single-resource invoke check the
	// playground's mayInvoke applies, rather than being trusted because it came
	// from a signed-in session.
	if !fromHandle {
		invokable := make([]string, 0, len(mentions))
		for _, rid := range mentions {
			if d.canAccess(r, rid, false) {
				invokable = append(invokable, rid)
			} else {
				reasons[rid] = reasonForbidden
			}
		}
		mentions = invokable
	}

	// The run receives the instruction, not the addressing: strip the handles
	// that name a mentioned resource. The stored message keeps the text as
	// typed, so the transcript still shows who was addressed.
	prompt := stripMentionTokens(body.Body, d.mentionTokens(mentions))

	// A typed @handle is a direct request to a named agent: it goes straight to
	// the hub, exactly like the playground's message/send. The thread stream
	// follows the task id returned here, so the reply arrives as it completes.
	// Routing it instead turns the request into an asynchronous ticket whose new
	// task id the stream never learns about, leaving the console with no reply
	// even when the agent answered. Only plain messages (no @handle in the body)
	// are candidates for the scheduler's ticket / pass-through model.
	if d.Router != nil && len(handles) == 0 {
		trig := &router.Trigger{
			Source:    string(router.SourceThread),
			SourceID:  id + "#" + userMsg.ID,
			ThreadID:  id,
			ActorID:   p.UserID,
			ActorName: p.Name,
			ActorKind: router.ActorHuman,
			Kind:      "message",
			Body:      prompt,
			Explicit:  len(mentions) > 0,
			At:        time.Now().UTC(),
			DueAt:     time.Now().UTC(),
		}
		if len(mentions) > 0 {
			trig.Meta = map[string]any{"mentions": mentions, "resource": mentions[0]}
		}
		created, _, rerr := d.Router.Inject(r.Context(), trig)
		if rerr != nil {
			httputil.WriteErr(w, rerr)
			return
		}
		if routerAdmitted(created) {
			httputil.WriteJSON(w, http.StatusCreated, map[string]any{
				"message":   userMsg,
				"delegated": []string{},
				"failed":    []string{},
				"routed":    true,
				"reason":    "",
				"reasons":   reasons,
			})
			return
		}
		// The scheduler dropped this trigger (source off, policy unset or an
		// allowlist that excludes the actor). A direct request from a person must
		// not disappear silently, so fall through to the direct delegation below
		// instead of reporting a routing that will never produce a task.
	}

	delegated := make([]string, 0, len(mentions))
	failed := make([]string, 0)
	for _, rid := range mentions {
		params := a2a.MessageSendParams{Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(prompt))}
		// The hub resolves the run target from the params metadata; the message
		// metadata is kept too so the history records which agent was addressed.
		params.Metadata = map[string]any{"resourceId": rid}
		params.Message.Metadata = map[string]any{"resourceId": rid}
		task, derr := d.Hub.Send(r.Context(), params)
		if derr != nil {
			failed = append(failed, rid)
			reasons[rid] = reasonDelegateFailed
			continue
		}
		if _, uerr := d.Threads.UpsertAgentMessage(r.Context(), id, task.ID, rid, ""); uerr != nil {
			// Non-fatal: the SSE stream still surfaces the reply as it arrives.
			continue
		}
		delegated = append(delegated, task.ID)
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"message":   userMsg,
		"delegated": delegated,
		"failed":    failed,
		"reason":    primaryReason(reasons),
		"reasons":   reasons,
	})
}

// handleThreadEventStream replays a thread's history and then follows, over SSE,
// every delegated task: as each agent replies, the reply is persisted as an agent
// message and forwarded to the client.
func (d Deps) handleThreadEventStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := PrincipalFrom(r)
	if _, err := d.Threads.GetThread(r.Context(), id, p.UserID); err != nil {
		threadError(w, err)
		return
	}
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}

	msgs, err := d.Threads.ListMessages(r.Context(), id, 0)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	for _, m := range msgs {
		if err := httputil.WriteSSE(w, flusher, "thread.message", m); err != nil {
			return
		}
	}

	// Map each delegated task back to the agent (resource id) that produced it so
	// agent replies can be persisted with the right author.
	ridByTask := map[string]string{}
	for _, m := range msgs {
		if m.TaskID != "" {
			ridByTask[m.TaskID] = m.ResourceID
		}
	}
	ids := taskIDsOf(msgs)
	for _, tid := range ids {
		for _, ev := range d.Hub.Events(r.Context(), tid, 0, 0) {
			if m := d.applyTaskEvent(r.Context(), id, ev, ridByTask); m != nil {
				if err := httputil.WriteSSE(w, flusher, "thread.agent", m); err != nil {
					return
				}
			}
		}
	}

	merged, cancel := subscribeMerged(d.Hub.Broker(), ids)
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
		case ev, open := <-merged:
			if !open {
				return
			}
			if m := d.applyTaskEvent(r.Context(), id, ev, ridByTask); m != nil {
				if err := httputil.WriteSSE(w, flusher, "thread.agent", m); err != nil {
					return
				}
			}
		}
	}
}

// applyTaskEvent turns a hub event into a persisted agent message, or nil when
// the event carries nothing the thread should show.
func (d Deps) applyTaskEvent(ctx context.Context, threadID string, ev hub.Event, ridByTask map[string]string) *threads.Message {
	switch ev.Kind {
	case hub.EventKindMessage:
		if ev.Message == nil {
			return nil
		}
		body := ev.Message.Text()
		if body == "" {
			return nil
		}
		rid := ridByTask[ev.TaskID]
		m, _ := d.Threads.UpsertAgentMessage(ctx, threadID, ev.TaskID, rid, body)
		return m
	case hub.EventKindStatus:
		if ev.Status != nil && ev.Status.Status.State.IsTerminal() {
			// Mark completion without clobbering an existing reply.
			rid := ridByTask[ev.TaskID]
			m, _ := d.Threads.UpsertAgentMessage(ctx, threadID, ev.TaskID, rid, "")
			return m
		}
	}
	return nil
}

func taskIDsOf(msgs []*threads.Message) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.TaskID != "" && !seen[m.TaskID] {
			seen[m.TaskID] = true
			out = append(out, m.TaskID)
		}
	}
	return out
}

// subscribeMerged fans out a broker subscription per task into one channel and
// returns a cancel that tears them all down.
func subscribeMerged(b *hub.Broker, taskIDs []string) (<-chan hub.Event, func()) {
	out := make(chan hub.Event, 64)
	var wg sync.WaitGroup
	cancels := make([]func(), 0, len(taskIDs))
	for _, tid := range taskIDs {
		ch, cancel := b.Subscribe(tid, 64)
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func(ch <-chan hub.Event) {
			defer wg.Done()
			for ev := range ch {
				out <- ev
			}
		}(ch)
	}
	done := func() {
		for _, c := range cancels {
			c()
		}
		wg.Wait()
		close(out)
	}
	return out, done
}

// mentionHandles returns the @handles a person typed, in first-seen order and
// without the '@'. A handle starts at the beginning of the body or after
// whitespace and ends at the next whitespace: the same rule stripMentionTokens
// uses, so every token found here is one it can remove. An '@' inside a word
// (an email address, for instance) is not an address to an agent.
func mentionHandles(body string) []string {
	out := make([]string, 0)
	seen := make(map[string]bool)
	for i := 0; i < len(body); {
		if (i == 0 || isTokenSpace(body[i-1])) && body[i] == '@' {
			j := i + 1
			for j < len(body) && !isTokenSpace(body[j]) {
				j++
			}
			if j > i+1 {
				token := body[i+1 : j]
				key := strings.ToLower(token)
				if !seen[key] {
					seen[key] = true
					out = append(out, token)
				}
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// Machine-readable reasons a thread message could not be handed to the agent it
// addressed. They are a finite enum so the console can explain the failure
// instead of collapsing every case into "could not reach".
const (
	reasonNotFound       = "not_found"
	reasonNotApproved    = "not_approved"
	reasonForbidden      = "forbidden"
	reasonDelegateFailed = "delegate_failed"
)

// resolvableEntries keeps the approved gate and applies the same invoke check
// the A2A/playground path uses (mayInvoke -> canAccess). A list visibility
// filter is not a substitute: visibleEntries filters a whole snapshot at once
// and fails closed when the grant store hiccups, whereas canAccess answers the
// question a run actually asks about one resource. This narrow helper leaves
// visibleEntries' global semantics untouched for the seven endpoints that use it.
func (d Deps) resolvableEntries(r *http.Request, entries []*registry.Entry) []*registry.Entry {
	out := make([]*registry.Entry, 0, len(entries))
	for _, e := range registry.Discoverable(entries) {
		if d.canAccess(r, e.ID, false) {
			out = append(out, e)
		}
	}
	return out
}

func matchEntry(entries []*registry.Entry, handle string) *registry.Entry {
	for _, e := range entries {
		if e == nil {
			continue
		}
		if strings.EqualFold(e.ID, handle) || strings.EqualFold(e.Name, handle) ||
			(e.DisplayName != "" && strings.EqualFold(e.DisplayName, handle)) {
			return e
		}
	}
	return nil
}

type mentionResolution struct {
	IDs     []string
	Reasons map[string]string
}

// resolveMentions maps @handles to the ids of approved resources this caller may
// actually invoke, and explains every handle it had to drop. Matching ignores
// case because a hand-typed handle does.
func (d Deps) resolveMentions(r *http.Request, handles []string) mentionResolution {
	res := mentionResolution{
		IDs:     make([]string, 0, len(handles)),
		Reasons: make(map[string]string),
	}
	seen := make(map[string]bool)
	if d.Reg == nil {
		for _, handle := range handles {
			res.Reasons[handle] = reasonNotFound
		}
		return res
	}
	all := d.Reg.Snapshot()
	invokable := d.resolvableEntries(r, all)
	approved := registry.Discoverable(all)
	for _, handle := range handles {
		if e := matchEntry(invokable, handle); e != nil {
			if !seen[e.ID] {
				seen[e.ID] = true
				res.IDs = append(res.IDs, e.ID)
			}
			continue
		}
		switch {
		case matchEntry(all, handle) == nil:
			res.Reasons[handle] = reasonNotFound
		case matchEntry(approved, handle) == nil:
			res.Reasons[handle] = reasonNotApproved
		default:
			res.Reasons[handle] = reasonForbidden
		}
	}
	return res
}

// resolveMentionHandles is the id-only view kept for callers that do not surface
// a reason.
func (d Deps) resolveMentionHandles(r *http.Request, handles []string) []string {
	return d.resolveMentions(r, handles).IDs
}

// primaryReason picks one deterministic reason out of a set: the most
// actionable explanation wins regardless of map iteration order.
func primaryReason(reasons map[string]string) string {
	for _, want := range []string{reasonForbidden, reasonNotApproved, reasonDelegateFailed, reasonNotFound} {
		for _, got := range reasons {
			if got == want {
				return want
			}
		}
	}
	return ""
}

// routerAdmitted reports whether a freshly injected trigger will actually reach
// a ticket. Inject returns the trigger, not the ticket, so created != nil alone
// also covers a trigger the program layer filtered out: that one never opens a
// run, and treating it as routed leaves the console waiting for a reply forever.
func routerAdmitted(created *router.Trigger) bool {
	return created != nil && created.Disposition != string(router.DispositionFiltered)
}

// mentionTokens collects every spelling a thread body may use for the resources it
// addresses: the id the console sends, the registry name, and the display label.
// Matching is case-insensitive because a hand-typed mention is.
func (d Deps) mentionTokens(ids []string) map[string]struct{} {
	tokens := make(map[string]struct{}, len(ids)*3)
	for _, id := range ids {
		if id == "" {
			continue
		}
		tokens[strings.ToLower(id)] = struct{}{}
		if d.Reg == nil {
			continue
		}
		if e, ok := d.Reg.Get(id); ok && e != nil {
			if e.Name != "" {
				tokens[strings.ToLower(e.Name)] = struct{}{}
			}
			if e.DisplayName != "" {
				tokens[strings.ToLower(e.DisplayName)] = struct{}{}
			}
		}
	}
	return tokens
}

// stripMentionTokens removes the @handles that name a mentioned resource, so a
// delegated run receives the instruction rather than the addressing. Only a
// handle at the start of the body or after whitespace counts, which leaves an
// address written inside a word alone. A body made of nothing but mentions keeps
// its text: an empty prompt would be worse than the token itself.
func stripMentionTokens(body string, tokens map[string]struct{}) string {
	if len(tokens) == 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		if (i == 0 || isTokenSpace(body[i-1])) && body[i] == '@' {
			j := i + 1
			for j < len(body) && !isTokenSpace(body[j]) {
				j++
			}
			if _, ok := tokens[strings.ToLower(body[i+1:j])]; ok && j > i+1 {
				i = j
				continue
			}
		}
		b.WriteByte(body[i])
		i++
	}
	if stripped := strings.TrimSpace(b.String()); stripped != "" {
		return stripped
	}
	return body
}

func isTokenSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// fillPendingReplies persists the reply body of every agent placeholder whose task
// has already answered, replacing the placeholder in place. The SSE stream is the
// live path; a read must not depend on somebody having been subscribed at the
// moment the agent finished, or the reply stays blank for good.
func (d Deps) fillPendingReplies(ctx context.Context, threadID string, msgs []*threads.Message) {
	if d.Hub == nil || d.Hub.Store() == nil || d.Threads == nil {
		return
	}
	for i, m := range msgs {
		if m == nil || m.TaskID == "" || m.Body != "" || m.AuthorType == "user" {
			continue
		}
		body := taskReply(d.Hub.Store().Get(ctx, m.TaskID))
		if body == "" {
			continue
		}
		updated, err := d.Threads.UpsertAgentMessage(ctx, threadID, m.TaskID, m.ResourceID, body)
		if err == nil && updated != nil {
			msgs[i] = updated
		}
	}
}

// taskReply is the agent's answer for a finished task: the last non-empty text
// message the agent wrote. A task still in flight has no reply to persist yet.
// History is the primary source, but a terminal status message and text
// artifacts are valid A2A answers too; trusting History alone left replies that
// only ever existed in one of those two places blank for good.
func taskReply(task *a2a.Task) string {
	if task == nil || !task.Status.State.IsTerminal() {
		return ""
	}
	for i := len(task.History) - 1; i >= 0; i-- {
		if task.History[i].Role == a2a.RoleUser {
			continue
		}
		if text := strings.TrimSpace(task.History[i].Text()); text != "" {
			return text
		}
	}
	if task.Status.Message != nil {
		if text := strings.TrimSpace(task.Status.Message.Text()); text != "" {
			return text
		}
	}
	for i := len(task.Artifacts) - 1; i >= 0; i-- {
		var b strings.Builder
		for _, p := range task.Artifacts[i].Parts {
			if p.Type == a2a.PartTypeText {
				b.WriteString(p.Text)
			}
		}
		if text := strings.TrimSpace(b.String()); text != "" {
			return text
		}
	}
	return ""
}

// threadError turns a threads store error into a response: a missing or
// not-owned thread is 404 (it must not be distinguishable from "does not exist"),
// everything else goes through the shared domain-error mapping.
func threadError(w http.ResponseWriter, err error) {
	if errors.Is(err, threads.ErrNotFound) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	writeDomainErr(w, err)
}
