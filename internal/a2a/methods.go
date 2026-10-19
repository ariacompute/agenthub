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
type TaskQueryParams struct {
	ID            string         `json:"id"`
	HistoryLength *int           `json:"historyLength,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// TaskResumeParams continues a task parked in input-required.
type TaskResumeParams struct {
	ID       string         `json:"id"`
	Message  Message        `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskPushNotificationConfigParams is the payload of tasks/pushNotification/set.
type TaskPushNotificationConfigParams struct {
	TaskID                string                  `json:"taskId"`
	PushNotification      *PushNotificationConfig `json:"pushNotificationConfig,omitempty"`
	PushNotificationAlias *PushNotificationConfig `json:"pushNotification,omitempty"`
}

// PushNotification resolves either accepted field name for the config.
func (p TaskPushNotificationConfigParams) Config() *PushNotificationConfig {
	if p.PushNotification != nil {
		return p.PushNotification
	}
	return p.PushNotificationAlias
}
