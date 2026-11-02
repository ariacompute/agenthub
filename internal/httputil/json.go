// Package httputil centralises JSON responses, machine readable errors and SSE
// streaming for every HTTP endpoint in agenthub.
//
// Errors never carry human prose: the wire format is always {"code":"..."} so
// the console can map a stable identifier onto its own localised copy.
package httputil

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ErrorBody is the only error shape produced by agenthub.
type ErrorBody struct {
	Code string `json:"code"`
}

// Code is the machine readable identifier used by every agenthub error.
type Code string

const (
	CodeBadRequest       Code = "bad_request"
	CodeUnauthorized     Code = "unauthorized"
	CodeNotFound         Code = "not_found"
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeInternal         Code = "internal_error"
	CodePayloadTooLarge  Code = "payload_too_large"

	// Identity and authorization. The console localises these, so the strings
	// are the stable contract even where a status code would have been enough.
	CodeUnauthenticated    Code = "unauthenticated"
	CodeForbidden          Code = "forbidden"
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeEmailTaken         Code = "email_taken"
	CodeWeakPassword       Code = "weak_password"
	CodeInvalidEmail       Code = "invalid_email"
	CodeUserDisabled       Code = "user_disabled"
	CodePasswordRequired   Code = "password_required"
	CodeSessionExpired     Code = "session_expired"
	CodeAccountLocked      Code = "account_locked"
	CodeOAuthNotConfigured Code = "oauth_not_configured"
	CodeOAuthStateMismatch Code = "oauth_state_mismatch"
	CodeOAuthFailed        Code = "oauth_failed"
	CodeOAuthNoEmail       Code = "oauth_no_email"
	// Administration.
	CodeRoleExists        Code = "role_already_exists"
	CodeGroupExists       Code = "group_already_exists"
	CodeBuiltinRole       Code = "builtin_role"
	CodeUnknownPermission Code = "unknown_permission"
	CodeInvalidGrant      Code = "invalid_grant"
	// Backups: database checkpoints and restores. A restore rewrites every
	// table, so each of these says which guardrail stopped it rather than
	// "something went wrong".
	CodeBackupsDisabled       Code = "backups_disabled"
	CodeBackupNotFound        Code = "backup_not_found"
	CodeBackupInProgress      Code = "backup_in_progress"
	CodeBackupInterrupted     Code = "backup_interrupted"
	CodeBackupEngineUnavail   Code = "backup_engine_unavailable"
	CodeBackupEngineFailed    Code = "backup_engine_failed"
	CodeBackupChecksum        Code = "backup_checksum_mismatch"
	CodeBackupSchemaMismatch  Code = "backup_schema_mismatch"
	CodeBackupScheduleInvalid Code = "backup_schedule_invalid"
	CodeBackupInvalidArchive  Code = "backup_invalid_archive"
	CodeBackupTableGuarded    Code = "backup_table_guarded"
	CodeBackupFailed          Code = "backup_failed"
	CodeRestoreConfirm        Code = "restore_confirm_required"
	CodeRestoreBusy           Code = "restore_busy"
	CodeRestoreInProgress     Code = "restore_in_progress"
	CodeRestoreFailed         Code = "restore_failed"

	// API keys: programmatic credentials. Each code names the guardrail that
	// stopped the call, which is more useful to a script than a generic failure.
	CodeAPIKeyNotFound     Code = "api_key_not_found"
	CodeAPIKeyInvalidScope Code = "api_key_invalid_scope"
	CodeAPIKeyExpired      Code = "api_key_expired"
	CodeAPIKeyRevoked      Code = "api_key_revoked"
	CodeAPIKeyNameRequired Code = "api_key_name_required"

	// Channels: inbound webhooks and outbound delivery. Each code names the
	// guardrail that stopped the hop so the console can localise it precisely.
	CodeChannelDisabled         Code = "channel_disabled"
	CodeChannelNotFound         Code = "channel_not_found"
	CodeChannelSignatureMissing Code = "channel_signature_missing"
	CodeChannelSignatureInvalid Code = "channel_signature_invalid"
	CodeChannelTimestampExpired Code = "channel_timestamp_expired"
	CodeChannelPayloadInvalid   Code = "channel_payload_invalid"
	CodeDeliveryFailed          Code = "delivery_failed"
	// A retry only makes sense for one receipt that never left. Naming the two
	// refusals separately lets the console hide a button that cannot work instead
	// of discovering it on click.
	CodeDeliveryNotFound     Code = "delivery_not_found"
	CodeDeliveryNotRetryable Code = "delivery_not_retryable"

	// Connectors: external applications invoking a kind=mcp resource.
	CodeConnectorNotFound  Code = "connector_not_found"
	CodeConnectorDenied    Code = "connector_action_denied"
	CodeConnectorPolicyOff Code = "connector_policy_off"

	// Speech: voice messages transcribed and synthesised server side.
	CodeAudioTooLarge         Code = "audio_too_large"
	CodeAudioUnsupported      Code = "audio_unsupported"
	CodeAudioTranscribeFailed Code = "audio_transcribe_failed"
	CodeSpeechDisabled        Code = "speech_disabled"

	// Computers, schedules and knowledge: agent capabilities (F10-F16). Each code
	// names the guardrail that stopped the call so the console can localise it.
	CodeComputerNotFound     Code = "computer_not_found"
	CodeScheduleNotFound     Code = "schedule_not_found"
	CodeScheduleInvalid      Code = "schedule_invalid"
	CodeSkillDistillDisabled Code = "skill_distill_disabled"
	// FeatureUnavailable is the single sentinel for any agent-capability endpoint
	// whose store is not wired in (computers / schedules / knowledge). It replaces
	// the previous computer_disabled / knowledge_unavailable split.
	CodeFeatureUnavailable Code = "feature_unavailable"

	// Router: the scheduler. Each code names the guardrail that refused the
	// command, so the console can say why rather than "something went wrong".
	CodeRouterTicketNotFound    Code = "router_ticket_not_found"
	CodeRouterTriggerNotFound   Code = "router_trigger_not_found"
	CodeRouterThreadNotFound    Code = "router_thread_not_found"
	CodeRouterTicketClosed      Code = "router_ticket_closed"
	CodeRouterHandbackExhausted Code = "router_handback_exhausted"
	CodeRouterPolicyInvalid     Code = "router_policy_invalid"
	CodeRouterResourceUnset     Code = "router_resource_unset"

	// Sandbox: execution environments. Two of these deliberately look alike and
	// are not: FeatureUnavailable means the control plane is not wired in,
	// SandboxUnsupported means it is wired in but the provider cannot run here
	// (no docker socket, no bwrap binary). The first is an operator decision,
	// the second is an environment problem, and the console says so.
	CodeSandboxDisabled        Code = "sandbox_disabled"
	CodeSandboxUnsupported     Code = "sandbox_unsupported"
	CodeSandboxUnknownProvider Code = "sandbox_unknown_provider"
	CodeSandboxPolicyDenied    Code = "sandbox_policy_denied"
	CodeSandboxTimeout         Code = "sandbox_timeout"
	CodeSandboxLeaseLost       Code = "sandbox_lease_lost"
	CodeSandboxLimitReached    Code = "sandbox_limit_reached"
	CodeSandboxNodeOffline     Code = "sandbox_node_offline"
	CodeSandboxNotFound        Code = "sandbox_not_found"
)

// Error is a sentinel error carrying a machine readable code.
type Error struct{ Code Code }

func (e *Error) Error() string { return string(e.Code) }

// AsError unwraps any error into an *Error with a stable code.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal}
}

// WriteJSON serialises v with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", "error", err)
	}
}

// WriteError writes {"code": ...} with the matching status code.
func WriteError(w http.ResponseWriter, status int, code Code) {
	WriteJSON(w, status, ErrorBody{Code: string(code)})
}

// WriteErr maps a domain error onto an HTTP status plus its machine readable code.
func WriteErr(w http.ResponseWriter, err error) {
	e := AsError(err)
	switch e.Code {
	case CodeBadRequest, CodeWeakPassword, CodeInvalidEmail, CodeInvalidGrant,
		CodeUnknownPermission, CodePasswordRequired:
		WriteError(w, http.StatusBadRequest, e.Code)
	// An expired or revoked session is not "unauthorised" in the HTTP sense:
	// the console treats 401 as "log in again", which is exactly what it wants.
	case CodeUnauthorized, CodeUnauthenticated, CodeSessionExpired, CodeInvalidCredentials:
		WriteError(w, http.StatusUnauthorized, e.Code)
	// A bad or missing webhook signature is an authentication failure: the console
	// treats 401 as "the channel secret is wrong", not "retry blindly".
	case CodeChannelSignatureMissing, CodeChannelSignatureInvalid, CodeChannelTimestampExpired:
		WriteError(w, http.StatusUnauthorized, e.Code)
	case CodeAccountLocked:
		WriteError(w, http.StatusTooManyRequests, e.Code)
	case CodeForbidden, CodeUserDisabled, CodeBuiltinRole:
		WriteError(w, http.StatusForbidden, e.Code)
	case CodeNotFound, CodeDeliveryNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	// A receipt that landed (or was never outbound) is a state refusal: the
	// request was well formed, re-posting it would duplicate the message.
	case CodeDeliveryNotRetryable:
		WriteError(w, http.StatusConflict, e.Code)
	case CodeMethodNotAllowed:
		WriteError(w, http.StatusMethodNotAllowed, e.Code)
	case CodePayloadTooLarge:
		WriteError(w, http.StatusRequestEntityTooLarge, e.Code)
	case CodeEmailTaken, CodeRoleExists, CodeGroupExists:
		WriteError(w, http.StatusConflict, e.Code)
	case CodeOAuthNotConfigured, CodeOAuthStateMismatch, CodeOAuthFailed, CodeOAuthNoEmail:
		// The provider's own failure text never reaches the caller: an operator
		// debugs that from the logs, the console only needs the class.
		WriteError(w, http.StatusBadGateway, e.Code)
	// Backups. "backups_disabled" is a 409 rather than a 404: the endpoint
	// exists, it is the instance that has no backup volume mounted.
	case CodeBackupsDisabled, CodeBackupInProgress, CodeBackupChecksum,
		CodeBackupSchemaMismatch, CodeBackupInvalidArchive, CodeBackupTableGuarded:
		WriteError(w, http.StatusConflict, e.Code)
	case CodeBackupNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	case CodeBackupScheduleInvalid, CodeRestoreConfirm:
		WriteError(w, http.StatusBadRequest, e.Code)
	case CodeBackupEngineUnavail, CodeRestoreBusy, CodeRestoreInProgress:
		WriteError(w, http.StatusServiceUnavailable, e.Code)
	case CodeBackupEngineFailed, CodeBackupFailed, CodeBackupInterrupted, CodeRestoreFailed:
		WriteError(w, http.StatusInternalServerError, e.Code)
	case CodeAPIKeyNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	case CodeAPIKeyNameRequired, CodeAPIKeyInvalidScope:
		WriteError(w, http.StatusBadRequest, e.Code)
	// An expired or revoked key is an authentication failure, so a script treats
	// it like a stale session and re-issues rather than retrying blindly.
	case CodeAPIKeyExpired, CodeAPIKeyRevoked:
		WriteError(w, http.StatusUnauthorized, e.Code)
	// Computers, schedules and knowledge (F10-F16).
	case CodeComputerNotFound, CodeScheduleNotFound, CodeRouterTicketNotFound,
		CodeRouterTriggerNotFound, CodeRouterThreadNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	case CodeSkillDistillDisabled:
		WriteError(w, http.StatusForbidden, e.Code)
	case CodeScheduleInvalid, CodeRouterPolicyInvalid:
		WriteError(w, http.StatusBadRequest, e.Code)
	// A closed ticket or an exhausted hand-back is a conflict: the state refuses
	// the command, the command itself was well formed.
	case CodeRouterTicketClosed, CodeRouterHandbackExhausted, CodeRouterResourceUnset:
		WriteError(w, http.StatusConflict, e.Code)
	// Sandbox. "unsupported" is a conflict rather than a 500: retrying the same
	// request against the same host will fail the same way until an operator
	// installs the runtime or points the provider elsewhere.
	case CodeSandboxDisabled, CodeSandboxPolicyDenied:
		WriteError(w, http.StatusForbidden, e.Code)
	case CodeSandboxUnsupported, CodeSandboxLeaseLost:
		WriteError(w, http.StatusConflict, e.Code)
	case CodeSandboxUnknownProvider:
		WriteError(w, http.StatusBadRequest, e.Code)
	case CodeSandboxNotFound:
		WriteError(w, http.StatusNotFound, e.Code)
	case CodeSandboxLimitReached:
		WriteError(w, http.StatusTooManyRequests, e.Code)
	case CodeSandboxNodeOffline:
		WriteError(w, http.StatusServiceUnavailable, e.Code)
	case CodeSandboxTimeout:
		WriteError(w, http.StatusGatewayTimeout, e.Code)
	case CodeFeatureUnavailable:
		WriteError(w, http.StatusServiceUnavailable, e.Code)
	default:
		WriteError(w, http.StatusInternalServerError, e.Code)
	}
}

// Decode reads a bounded JSON body into dst. Oversized or malformed bodies
// always surface as an *Error with a machine readable code.
func Decode(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Code: CodePayloadTooLarge}
		}
		// Everything else (empty body, malformed JSON, unknown field) is the
		// caller's fault in exactly the same way: one code says it.
		return &Error{Code: CodeBadRequest}
	}
	return nil
}
