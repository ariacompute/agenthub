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
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"thread": t, "messages": msgs})
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

	delegated := make([]string, 0, len(body.Mentions))
	failed := make([]string, 0)
	for _, rid := range body.Mentions {
		params := a2a.MessageSendParams{Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(body.Body))}
		params.Message.Metadata = map[string]any{"resourceId": rid}
		task, derr := d.Hub.Send(r.Context(), params)
		if derr != nil {
			failed = append(failed, rid)
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
		for _, ev := range d.Hub.Events(tid, 0, 0) {
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
