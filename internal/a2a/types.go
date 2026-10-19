// Package a2a is the frozen A2A (Agent2Agent) wire contract.
//
// Nothing outside this package is allowed to reshape a frame: handlers, hub and
// adapters marshal these types as-is so that any A2A conforming client can talk
// to agenthub. Streaming has no sentinel frame — a run ends with the
// completed / failed / canceled (or input-required) status update.
package a2a

import "time"

// TaskState enumerates the A2A task lifecycle.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateUnknown       TaskState = "unknown"
)

// States is every valid task state, in lifecycle order.
var States = []TaskState{
	TaskStateSubmitted,
	TaskStateWorking,
	TaskStateInputRequired,
	TaskStateCompleted,
	TaskStateFailed,
	TaskStateCanceled,
	TaskStateUnknown,
}

// IsTerminal reports whether no further transition may leave this state.
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled:
		return true
	default:
		return false
	}
}

// Valid reports whether the state is part of the A2A lifecycle.
func (s TaskState) Valid() bool {
	for _, c := range States {
		if c == s {
			return true
		}
	}
	return false
}

// TerminalStates lists the states that end a run.
var TerminalStates = []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateCanceled}

// Part kinds.
const (
	PartTypeText string = "text"
	PartTypeFile string = "file"
	PartTypeData string = "data"
)

// FileContent is the payload of a FilePart (inline bytes or a URI).
type FileContent struct {
	Name     *string `json:"name,omitempty"`
	MimeType *string `json:"mimeType,omitempty"`
	Bytes    *string `json:"bytes,omitempty"`
	URI      *string `json:"uri,omitempty"`
}

// Part is the A2A discriminated union, encoded with a "type" discriminator.
type Part struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	File     *FileContent   `json:"file,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// NewTextPart builds a text part.
func NewTextPart(text string) Part {
	return Part{Type: PartTypeText, Text: text}
}

// NewDataPart builds a structured data part.
func NewDataPart(data map[string]any) Part {
	return Part{Type: PartTypeData, Data: data}
}

// Message roles.
const (
	RoleUser  string = "user"
	RoleAgent string = "agent"
)

// Message is a single turn exchanged between a client and agenthub.
type Message struct {
	Kind      string         `json:"kind,omitempty"`
	Role      string         `json:"role"`
	Parts     []Part         `json:"parts"`
	MessageID string         `json:"messageId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	SessionID string         `json:"sessionId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// NewMessage builds a message with the given role and parts.
func NewMessage(role string, parts ...Part) Message {
	return Message{Kind: "message", Role: role, Parts: parts}
}

// Text concatenates the text of every text part.
func (m Message) Text() string {
	var b []byte
	for _, p := range m.Parts {
		if p.Type == PartTypeText {
			b = append(b, p.Text...)
		}
	}
	return string(b)
}

// Context returns the A2A v1.0 context id, falling back to the v0.1 session id.
func (m Message) Context() string {
	if m.ContextID != "" {
		return m.ContextID
	}
	return m.SessionID
}

// TaskStatus is the current lifecycle state of a task plus an optional message.
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"`
}

// Artifact is a named, appendable output produced while running a task.
type Artifact struct {
	ArtifactID  string         `json:"artifactId,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Index       int            `json:"index"`
	Append      *bool          `json:"append,omitempty"`
	LastChunk   *bool          `json:"lastChunk,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Task is the unit of work exchanged over A2A.
type Task struct {
	Kind       string         `json:"kind,omitempty"`
	ID         string         `json:"id"`
	ContextID  string         `json:"contextId,omitempty"`
	SessionID  string         `json:"sessionId,omitempty"`
	Status     TaskStatus     `json:"status"`
	Artifacts  []Artifact     `json:"artifacts,omitempty"`
	History    []Message      `json:"history,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	ResourceID string         `json:"resourceId,omitempty"`
}

// NewTask builds a submitted task for the given inbound message.
func NewTask(id string, msg Message, resourceID string) *Task {
	return &Task{
		Kind:       "task",
		ID:         id,
		ContextID:  msg.Context(),
		Status:     TaskStatus{State: TaskStateSubmitted, Timestamp: Timestamp(time.Now())},
		Artifacts:  nil,
		History:    []Message{msg},
		ResourceID: resourceID,
	}
}

// Kinds of streaming envelopes emitted on message/stream.
const (
	KindTask            string = "task"
	KindMessage         string = "message"
	KindStatusUpdate    string = "status-update"
	KindArtifactUpdate  string = "artifact-update"
	EventTaskStatus     string = "task_status_update"
	EventTaskArtifact   string = "task_artifact_update"
	EventTaskMessage    string = "message"
	EventTaskSpan       string = "task_span_update"
	EventTerminalStatus string = "task_terminal"
)

// TaskStatusUpdateEvent advances the lifecycle of a task on a stream.
type TaskStatusUpdateEvent struct {
	Kind      string         `json:"kind"`
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Final     bool           `json:"final"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TaskArtifactUpdateEvent streams one artifact (or one appended chunk).
type TaskArtifactUpdateEvent struct {
	Kind      string         `json:"kind"`
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId,omitempty"`
	Artifact  Artifact       `json:"artifact"`
	Append    *bool          `json:"append,omitempty"`
	LastChunk *bool          `json:"lastChunk,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// PushNotificationConfig is the client callback registered for a task.
type PushNotificationConfig struct {
	ID             string          `json:"id,omitempty"`
	URL            string          `json:"url"`
	Token          string          `json:"token,omitempty"`
	Authentication *Authentication `json:"authentication,omitempty"`
}

// Authentication describes how a peer must authenticate.
type Authentication struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials,omitempty"`
}

// AgentCapabilities advertises optional A2A features.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// AgentSkill is one advertised capability of an A2A agent.
type AgentSkill struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Tags         []string `json:"tags,omitempty"`
	Examples     []string `json:"examples,omitempty"`
	InputModes   []string `json:"inputModes,omitempty"`
	OutputModes  []string `json:"outputModes,omitempty"`
	ResourceKind string   `json:"resourceKind,omitempty"`
}

// AgentProvider identifies who operates the agent.
type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

// AgentCard is the discovery document served at /.well-known/agent.json.
type AgentCard struct {
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	URL                string            `json:"url"`
	Version            string            `json:"version"`
	ProtocolVersion    string            `json:"protocolVersion"`
	Capabilities       AgentCapabilities `json:"capabilities"`
	DefaultInputModes  []string          `json:"defaultInputModes"`
	DefaultOutputModes []string          `json:"defaultOutputModes"`
	Skills             []AgentSkill      `json:"skills"`
	Authentication     *Authentication   `json:"authentication,omitempty"`
	Provider           *AgentProvider    `json:"provider,omitempty"`
	DocumentationURL   string            `json:"documentationUrl,omitempty"`
	Metadata           map[string]any    `json:"metadata,omitempty"`
}

// Timestamp renders a time in RFC3339 for wire payloads.
func Timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
