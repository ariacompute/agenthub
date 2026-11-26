package router

// The semantic layer may only upgrade an open question. These cases pin the merge
// rule itself: a model answer for a trigger the rules already settled is ignored,
// a malformed or unavailable answer changes nothing, and a short answer cannot
// shift the following triggers onto the wrong verdict.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubAssessor struct {
	res []Decision
	err error
}

func (s stubAssessor) Assess(context.Context, []*Trigger) ([]Decision, error) {
	return s.res, s.err
}

func implicitTrigger(id string) *Trigger {
	return &Trigger{ID: id, Source: string(SourceThread), ActorKind: ActorAgent}
}

func TestSemanticVerdictOnlyUpgradesAnOpenQuestion(t *testing.T) {
	now := time.Now().UTC()
	batch := []*Trigger{
		implicitTrigger("implicit"),
		{ID: "explicit", Source: string(SourceThread), Explicit: true},
	}

	e := NewEngine(Options{
		Store: NewMemoryStore(), Bus: NewBus(), Clock: func() time.Time { return now },
		Assessor: stubAssessor{res: []Decision{
			{Kind: string(DecisionAccept), By: "model"},
			{Kind: string(DecisionIgnore), By: "model"},
		}},
	})
	got := e.decide(context.Background(), batch)
	if got[0].Kind != string(DecisionAccept) || got[0].By != "model" {
		t.Fatalf("implicit verdict = %+v, want the model's accept", got[0])
	}
	if got[1].Kind != string(DecisionAccept) || got[1].By != "rule" {
		t.Fatalf("explicit verdict = %+v, the model must not overturn a rule verdict", got[1])
	}
}

func TestSemanticVerdictsThatCannotBeUsedAreDropped(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		res  []Decision
	}{
		{"short answer", nil},
		{"unknown kind", []Decision{{Kind: "not-a-kind", By: "model"}}},
		{"unavailable", []Decision{{Kind: string(DecisionUnavailable), By: "model"}}},
	}
	for _, c := range cases {
		e := NewEngine(Options{
			Store: NewMemoryStore(), Bus: NewBus(), Clock: func() time.Time { return now },
			Assessor: stubAssessor{res: c.res},
		})
		got := e.decide(context.Background(), []*Trigger{implicitTrigger("t1")})
		if got[0].Kind != string(DecisionIgnore) || got[0].By != "rule" {
			t.Fatalf("%s: verdict = %+v, want the rule's implicit ignore", c.name, got[0])
		}
	}

	// A verdict without a reason gets the generic one, so the console never
	// shows an empty explanation.
	e := NewEngine(Options{
		Store: NewMemoryStore(), Bus: NewBus(), Clock: func() time.Time { return now },
		Assessor: stubAssessor{res: []Decision{{Kind: string(DecisionAccept), By: "model"}}},
	})
	got := e.decide(context.Background(), []*Trigger{implicitTrigger("t1")})
	if got[0].Reason != "model" {
		t.Fatalf("reason = %q, want the model default", got[0].Reason)
	}

	// An assessment failure keeps the rule verdicts: it is a lost upgrade, not
	// an outage.
	e = NewEngine(Options{
		Store: NewMemoryStore(), Bus: NewBus(), Clock: func() time.Time { return now },
		Assessor: stubAssessor{err: errors.New("model down")},
	})
	got = e.decide(context.Background(), []*Trigger{implicitTrigger("t1")})
	if got[0].By != "rule" {
		t.Fatalf("verdict after a failed assessment = %+v, want the rule verdict", got[0])
	}
}

func TestTitleOfFallsBackFromGoalToBodyToSource(t *testing.T) {
	cases := []struct {
		name string
		trig *Trigger
		want string
	}{
		{"goal", &Trigger{Meta: map[string]any{"goal": "  ship it  "}}, "ship it"},
		{"body", &Trigger{Body: "  a message  "}, "a message"},
		{"source and id", &Trigger{Source: "cron", SourceID: "sched-1"}, "cron sched-1"},
		{"source only", &Trigger{Source: "manual"}, "manual trigger"},
	}
	for _, c := range cases {
		if got := titleOf(c.trig); got != c.want {
			t.Errorf("%s: titleOf = %q, want %q", c.name, got, c.want)
		}
	}
	// A pasted body is cut to what a list row can show.
	long := titleOf(&Trigger{Body: strings.Repeat("x", 200)})
	if len([]rune(long)) != 80 {
		t.Fatalf("long title = %d runes, want 80", len([]rune(long)))
	}
}
