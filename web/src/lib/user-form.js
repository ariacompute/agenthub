// Client side view of the identity rules the server enforces, expressed as the
// machine codes it returns (invalid_email / weak_password) so the drawer can
// render them with the same copy as a rejection that came back over the wire.
//
// The rules are mirrored, never tightened: refusing an address or a password the
// backend would accept is worse than one extra round trip. The copy of each rule
// lives in internal/auth (ValidEmail, MinPasswordLength) and the comments below
// say which.
//
// Anything the client cannot know - an address already taken, a role deleted
// since the drawer opened - stays a server verdict, and the caller puts it in
// the same slot.

// auth.MinPasswordLength. Counted in bytes because Go's len() counts bytes: a
// password the server accepts must not be rejected here.
export const MIN_PASSWORD_BYTES = 8

// auth.ValidEmail: no whitespace, an @ that is neither first nor last, and a
// domain that contains a dot without starting or ending with one.
export function validEmail(value) {
  const email = String(value ?? '').trim()
  if (!email || /\s/.test(email)) return false
  const at = email.lastIndexOf('@')
  if (at <= 0 || at === email.length - 1) return false
  const domain = email.slice(at + 1)
  return domain.includes('.') && !domain.startsWith('.') && !domain.endsWith('.')
}

export function passwordBytes(value) {
  return new TextEncoder().encode(String(value ?? '')).length
}

/**
 * Returns a code per field, empty when the field is acceptable.
 *
 * `keepPassword` says the password box is intentionally blank because the
 * account already has one (editing a user without changing the password).
 */
export function userFieldErrors(form, { keepPassword = false } = {}) {
  const errors = { email: '', password: '' }
  if (!validEmail(form?.email)) errors.email = 'invalid_email'
  if (!keepPassword && passwordBytes(form?.password) < MIN_PASSWORD_BYTES) errors.password = 'weak_password'
  return errors
}
