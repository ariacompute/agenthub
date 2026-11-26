package handlers

// Every A2A method decodes its own params struct, so each one has its own
// invalid_params branch. A2A peers are other people's programs: the answer has
// to be an envelope error naming the argument, not a 500 from the hub.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"agenthub/internal/a2a"
)

func TestA2ARejectsParamsOfTheWrongShapePerMethod(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	for _, method := range []string{
		a2a.MethodMessageSend,
		a2a.MethodMessageStream,
		a2a.MethodTasksCancel,
		a2a.MethodTasksResume,
		a2a.MethodPushSet,
		a2a.MethodPushGet,
	} {
		t.Run(method, func(t *testing.T) {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":5}`, method)
			w := do(t, h, http.MethodPost, "/a2a", body, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 with an envelope error (body %s)", w.Code, w.Body.String())
			}
			var res struct {
				Error *struct {
					Code float64 `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatalf("decode: %v (%s)", err, w.Body.String())
			}
			if res.Error == nil || res.Error.Code != a2a.CodeInvalidParams {
				t.Fatalf("error = %+v, want invalid_params", res.Error)
			}
		})
	}
}

// TestA2AMessageSendWithoutAResourceIsRefused: a message that names no resource
// has to be resolved by the hub; when that fails the peer gets resource_not_found
// rather than a task that will never run.
func TestA2AMessageSendWithoutAResourceIsRefused(t *testing.T) {
	h := Register(testDeps(t, openConfig("")))

	params := a2a.MessageSendParams{Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hi"))}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": a2a.JSONRPCVersion,
		"id":      11,
		"method":  a2a.MethodMessageSend,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := do(t, h, http.MethodPost, "/a2a", string(payload), nil)
	var res struct {
		Error *struct {
			Code    float64 `json:"code"`
			Message string  `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if res.Error == nil {
		t.Fatalf("a message with no resource returned a result: %s", w.Body.String())
	}
	if res.Error.Code != float64(a2a.CodeResourceNotFound) && res.Error.Message != a2a.MsgResourceNotFound {
		t.Fatalf("error = %+v, want resource_not_found", res.Error)
	}
}
