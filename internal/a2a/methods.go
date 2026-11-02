package a2a

import "strings"

// A2A method names (v1.0, unchanged since v0.2) plus the v0.1 aliases kept for
// compatibility.
const (
	MethodMessageSend   = "message/send"
	MethodMessageStream = "message/stream"
	MethodTasksGet      = "tasks/get"
	MethodTasksCancel   = "tasks/cancel"
	MethodTasksResume   = "tasks/resume"
	MethodPushSet       = "tasks/pushNotification/set"
	MethodPushGet       = "tasks/pushNotification/get"

	// v0.1 aliases, accepted when AGENTHUB_A2A_LEGACY_METHODS is enabled.
	MethodTasksSend          = "tasks/send"
	MethodTasksSendSubscribe = "tasks/sendSubscribe"
)

// legacyMethods maps a v0.1 method name onto its v1.0 equivalent.
var legacyMethods = map[string]string{
	MethodTasksSend:          MethodMessageSend,
	MethodTasksSendSubscribe: MethodMessageStream,
}

// Canonical resolves a method name. When legacy is false only the current
// (v1.0) names are accepted, which is how a strict deployment opts out of the
// aliases.
func Canonical(method string, legacy bool) (string, bool) {
	method = strings.TrimSpace(method)
	if method == "" {
		return "", false
	}
	switch method {
	case MethodMessageSend, MethodMessageStream, MethodTasksGet, MethodTasksCancel, MethodTasksResume, MethodPushSet, MethodPushGet:
		return method, true
	}
	if legacy {
		if canon, ok := legacyMethods[method]; ok {
			return canon, true
		}
	}
	return "", false
}

// Methods lists every method name exposed by agenthub (aliases excluded).
var Methods = []string{
	MethodMessageSend,
	MethodMessageStream,
	MethodTasksGet,
	MethodTasksCancel,
	MethodTasksResume,
	MethodPushSet,
	MethodPushGet,
}

// MessageSendConfiguration tunes one message/send call.
type MessageSendConfiguration struct {
	AcceptedOutputModes []string                `json:"acceptedOutputModes,omitempty"`
	HistoryLength       *int                    `json:"historyLength,omitempty"`
	Blocking            *bool                   `json:"blocking,omitempty"`
	PushNotification    *PushNotificationConfig `json:"pushNotification,omitempty"`
}

// MessageSendParams is the payload of message/send and message/stream.
type MessageSendParams struct {
	Message       Message                   `json:"message"`
	Configuration *MessageSendConfiguration `json:"configuration,omitempty"`
	Metadata      map[string]any            `json:"metadata,omitempty"`
}

// TaskQueryParams is the payload of tasks/get and tasks/cancel.
//
// TaskID is an accepted alias of ID. The A2A specification names this field `id`
// on the task methods and `taskId` on the push notification methods, which made
// a client that reused one payload struct fail on the other half of the pair.
// Both spellings are read everywhere and ID wins when a caller sends both.
type TaskQueryParams struct {
	ID            string         `json:"id"`
	TaskID        string         `json:"taskId,omitempty"`
	HistoryLength *int           `json:"historyLength,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// TaskResumeParams continues a task parked in input-required.
type TaskResumeParams struct {
	ID       string         `json:"id"`
	TaskID   string         `json:"taskId,omitempty"`
	Message  Message        `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskPushNotificationConfigParams is the payload of tasks/pushNotification/set.
type TaskPushNotificationConfigParams struct {
	TaskID                string                  `json:"taskId"`
	ID                    string                  `json:"id,omitempty"`
	PushNotification      *PushNotificationConfig `json:"pushNotificationConfig,omitempty"`
	PushNotificationAlias *PushNotificationConfig `json:"pushNotification,omitempty"`
}

// ResolveTaskID returns the task a params object addresses, accepting both
// spellings. canonical is preferred and the alias is the fallback, so a client
// that sends both can never be surprised by which one wins.
func ResolveTaskID(canonical, alias string) string {
	if canonical != "" {
		return canonical
	}
	return alias
}

// ResolveTaskID returns the task id of a tasks/get or tasks/cancel payload.
func (p TaskQueryParams) ResolveTaskID() string { return ResolveTaskID(p.ID, p.TaskID) }

// ResolveTaskID returns the task id of a tasks/resume payload.
func (p TaskResumeParams) ResolveTaskID() string { return ResolveTaskID(p.ID, p.TaskID) }

// ResolveTaskID returns the task id of a pushNotification payload.
func (p TaskPushNotificationConfigParams) ResolveTaskID() string {
	return ResolveTaskID(p.TaskID, p.ID)
}

// PushNotification resolves either accepted field name for the config.
func (p TaskPushNotificationConfigParams) Config() *PushNotificationConfig {
	if p.PushNotification != nil {
		return p.PushNotification
	}
	return p.PushNotificationAlias
}
