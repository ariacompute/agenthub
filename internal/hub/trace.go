package hub

import (
	"log/slog"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// Span statuses.
const (
	SpanStatusOK            = "ok"
	SpanStatusError         = "error"
	SpanStatusInputRequired = "input-required"
	SpanStatusSkipped       = "skipped"
)

// Span is one step of a run: which resource handled it, how long it took and
// how it ended. The console replays this list as the task timeline.
//
// start is the wall clock captured by StartSpan. It only lives for the flight
// time of a span, so it is never persisted and never crosses the wire.
type Span struct {
	ID         string `json:"id"`
	TaskID     string `json:"taskId"`
	ResourceID string `json:"resourceId,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Label      string `json:"label"`
	StartAt    string `json:"startAt"`
	EndAt      string `json:"endAt,omitempty"`
	DurationMS int64  `json:"durationMs"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`

	start time.Time
}

// StartSpan opens a span for one step of a run.
func (s *TaskStore) StartSpan(taskID, resourceID, kind, label string) Span {
	now := time.Now()
	return Span{
		ID:         registry.NewID("span"),
		TaskID:     taskID,
		ResourceID: resourceID,
		Kind:       kind,
		Label:      label,
		StartAt:    a2a.Timestamp(now),
		Status:     SpanStatusOK,
		start:      now,
	}
}

// FinishSpan closes a span and appends it to the task timeline.
func (s *TaskStore) FinishSpan(span Span, status, code string) Span {
	now := time.Now()
	span.EndAt = a2a.Timestamp(now)
	span.DurationMS = now.Sub(span.start).Milliseconds()
	span.Status = status
	span.Code = code

	if s.repo != nil {
		// A step of a run must never fail because its timeline entry could not
		// be written, so the failure is reported and the span still returns.
		if err := s.repo.AddSpan(dbCtx(), span); err != nil {
			slog.Warn("hub: persist span", "task", span.TaskID, "span", span.ID, "error", err)
		}
	} else {
		s.mu.Lock()
		if rec, ok := s.tasks[span.TaskID]; ok {
			rec.spans = append(rec.spans, span)
		}
		s.mu.Unlock()
	}

	out := span
	out.start = time.Time{}
	return out
}
