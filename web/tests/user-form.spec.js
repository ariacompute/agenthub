import { describe, expect, it } from 'vitest'
import { MIN_PASSWORD_BYTES, passwordBytes, userFieldErrors, validEmail } from '../src/lib/user-form.js'

// These rules mirror internal/auth. If one of them drifts, the console either
// refuses input the server would accept (a bug the user cannot work around) or
// sends something the server rejects after a round trip (the "no prompt" bug
// this suite exists to prevent). Every case below is written from the Go rule.
describe('validEmail (auth.ValidEmail)', () => {
  it('accepts the addresses the server accepts', () => {
    for (const email of ['a@example.com', 'first.last@sub.example.com', ' x@y.z ']) {
      expect(validEmail(email), email).toBe(true)
    }
  })

  it('rejects everything the server rejects', () => {
    for (const email of [
      '', // empty
      '   ', // whitespace only
      'nope', // no @
      '@example.com', // nothing before the @
      'user@', // nothing after the @
      'user@example', // domain without a dot
      'user@.example.com', // domain starts with the dot
      'user@example.', // domain ends with the dot
      'user name@example.com', // embedded whitespace
      'user@exam ple.com',
    ]) {
      expect(validEmail(email), email).toBe(false)
    }
  })

  it('survives a missing value', () => {
    expect(validEmail(undefined)).toBe(false)
    expect(validEmail(null)).toBe(false)
  })
})

describe('passwordBytes (auth.MinPasswordLength)', () => {
  it('counts bytes, not characters, exactly like Go len()', () => {
    expect(passwordBytes('test')).toBe(4)
    expect(passwordBytes('测试')).toBe(6)
    // Four Chinese characters are twelve bytes: the server accepts this, so the
    // console must not reject it.
    expect(passwordBytes('测试测试')).toBe(12)
    expect(MIN_PASSWORD_BYTES).toBe(8)
  })
})

describe('userFieldErrors', () => {
  it('names every bad field at once instead of stopping at the first', () => {
    expect(userFieldErrors({ email: 'nope', password: 'test' })).toEqual({
      email: 'invalid_email',
      password: 'weak_password',
    })
  })

  it('reports an empty password on create', () => {
    expect(userFieldErrors({ email: 'a@example.com', password: '' }).password).toBe('weak_password')
  })

  it('leaves an untouched password alone while editing', () => {
    const errors = userFieldErrors({ email: 'a@example.com', password: '' }, { keepPassword: true })
    expect(errors).toEqual({ email: '', password: '' })
  })

  it('still checks a password that was typed while editing', () => {
    const errors = userFieldErrors({ email: 'a@example.com', password: 'short' }, { keepPassword: false })
    expect(errors.password).toBe('weak_password')
  })

  it('accepts a form the server would accept', () => {
    expect(userFieldErrors({ email: 'user@example.com', password: 'test1234' })).toEqual({
      email: '',
      password: '',
    })
  })
})
