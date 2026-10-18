// The backend only ever returns machine readable codes. This module maps them
// onto localised copy keys; a code with no translation falls back to the code
// itself so a server string is never shown as if it were prose.
import { useCopy } from './use-copy.js'

const CODE_KEYS = {
  bad_request: 'badRequest',
  unauthorized: 'unauthorized',
  not_found: 'notFound',
  method_not_allowed: 'methodNotAllowed',
  payload_too_large: 'payloadTooLarge',
  internal_error: 'internalError',
  invalid_kind: 'invalidKind',
  invalid_health: 'invalidHealth',
  invalid_transport: 'invalidTransport',
  invalid_entry: 'invalidEntry',
  resource_not_found: 'resourceNotFound',
  resource_already_exists: 'resourceAlreadyExists',
  cli_command_not_allowed: 'cliCommandNotAllowed',
  adapter_timeout: 'adapterTimeout',
  resource_unreachable: 'resourceUnreachable',
  unknown_kind: 'unknownKind',
  task_not_found: 'taskNotFound',
  invalid_state_transition: 'invalidStateTransition',
  no_resource_available: 'noResourceAvailable',
  task_not_resumable: 'taskNotResumable',
  invalid_flow: 'invalidFlow',
  parse_error: 'parseError',
  invalid_request: 'invalidRequest',
  method_not_found: 'methodNotFound',
  invalid_params: 'invalidParams',
  task_not_cancelable: 'taskNotCancelable',
  push_notification_not_supported: 'pushNotificationNotSupported',
  unsupported_operation: 'unsupportedOperation',
  // Identity and authorization.
  unauthenticated: 'unauthenticated',
  forbidden: 'forbidden',
  invalid_credentials: 'invalidCredentials',
  email_taken: 'emailTaken',
  weak_password: 'weakPassword',
  invalid_email: 'invalidEmail',
  user_disabled: 'userDisabled',
  password_required: 'passwordRequired',
  session_expired: 'sessionExpired',
  oauth_not_configured: 'oauthNotConfigured',
  oauth_state_mismatch: 'oauthStateMismatch',
  oauth_failed: 'oauthFailed',
  oauth_no_email: 'oauthNoEmail',
  role_already_exists: 'roleAlreadyExists',
  group_already_exists: 'groupAlreadyExists',
  builtin_role: 'builtinRole',
  unknown_permission: 'unknownPermission',
  invalid_grant: 'invalidGrant',
}

/** Returns the localised message for a machine readable code. */
export function errorMessage(code) {
  if (!code) return ''
  const { t } = useCopy('errors')
  const key = CODE_KEYS[code]
  const message = key ? t(key) : ''
  return message || code
}
