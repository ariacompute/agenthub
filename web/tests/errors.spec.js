import { describe, expect, it } from 'vitest'
import { errorMessage } from '../src/lib/errors.js'
import { setLang } from '../src/lib/use-copy.js'

// The backend answers with machine readable codes. Whatever happens, the
// operator must never be shown a server string as if it were prose: an
// untranslated code is still better than a sentence nobody wrote.

describe('errorMessage', () => {
  it('localises every checkpoint code the restore guardrails can raise', () => {
    setLang('en')
    for (const code of [
      'backups_disabled',
      'backup_not_found',
      'backup_in_progress',
      'backup_interrupted',
      'backup_engine_unavailable',
      'backup_engine_failed',
      'backup_checksum_mismatch',
      'backup_schema_mismatch',
      'backup_schedule_invalid',
      'backup_invalid_archive',
      'backup_failed',
      'restore_confirm_required',
      'restore_busy',
      'restore_in_progress',
      'restore_failed',
    ]) {
      const message = errorMessage(code)
      expect(message, code).not.toBe(code)
      expect(message.length, code).toBeGreaterThan(0)
    }
  })

  it('follows the language', () => {
    setLang('en')
    const english = errorMessage('restore_confirm_required')
    setLang('zh')
    expect(errorMessage('restore_confirm_required')).not.toBe(english)
    setLang('en')
    expect(errorMessage('restore_confirm_required')).toBe(english)
  })

  it('hands back the code when it has no translation', () => {
    // A code from a newer server is the most useful thing to show: it can be
    // pasted into an issue, while a generic sentence cannot.
    expect(errorMessage('something_the_console_does_not_know')).toBe('something_the_console_does_not_know')
  })

  it('answers nothing when there is nothing to say', () => {
    expect(errorMessage('')).toBe('')
    expect(errorMessage(undefined)).toBe('')
    expect(errorMessage(null)).toBe('')
  })
})
