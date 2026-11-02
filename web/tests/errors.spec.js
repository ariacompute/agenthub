import { describe, expect, it } from 'vitest'
import { CODE_KEYS, errorMessage } from '../src/lib/errors.js'
import { setLang } from '../src/lib/use-copy.js'
import { errorsCopy } from '../src/lib/copy/errors-copy.js'

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

  it('localises the routing, channel, speech and sandbox codes', () => {
    // These arrived with the collaboration, router and sandbox work. Until they
    // were mapped the console printed sandbox_policy_denied at the operator,
    // which is a code, not an explanation.
    setLang('en')
    for (const code of [
      'account_locked',
      'feature_unavailable',
      'channel_disabled',
      'channel_not_found',
      'channel_signature_missing',
      'channel_signature_invalid',
      'channel_timestamp_expired',
      'channel_payload_invalid',
      'delivery_failed',
      'delivery_not_found',
      'delivery_not_retryable',
      'connector_not_found',
      'connector_action_denied',
      'connector_policy_off',
      'audio_too_large',
      'audio_unsupported',
      'audio_transcribe_failed',
      'speech_disabled',
      'computer_not_found',
      'schedule_not_found',
      'schedule_invalid',
      'skill_distill_disabled',
      'router_ticket_not_found',
      'router_trigger_not_found',
      'router_thread_not_found',
      'router_ticket_closed',
      'router_handback_exhausted',
      'router_policy_invalid',
      'router_resource_unset',
      'sandbox_disabled',
      'sandbox_unsupported',
      'sandbox_unknown_provider',
      'sandbox_policy_denied',
      'sandbox_timeout',
      'sandbox_lease_lost',
      'sandbox_limit_reached',
      'sandbox_node_offline',
      'sandbox_not_found',
    ]) {
      const message = errorMessage(code)
      expect(message, code).not.toBe(code)
      expect(message.length, code).toBeGreaterThan(0)
    }
  })

  it('maps every machine code onto a copy key, and nothing else', () => {
    // A one-way check would let an unused copy key rot; a two-way one keeps the
    // table and the taxonomy the same size, which is what makes the next code
    // addition fail loudly instead of degrading to a machine spelling.
    const mapped = new Set(Object.values(CODE_KEYS))
    const translated = new Set(Object.keys(errorsCopy.en))
    expect([...mapped].filter((key) => !translated.has(key))).toEqual([])
    expect([...translated].filter((key) => !mapped.has(key))).toEqual([])
  })

  it('answers nothing when there is nothing to say', () => {
    expect(errorMessage('')).toBe('')
    expect(errorMessage(undefined)).toBe('')
    expect(errorMessage(null)).toBe('')
  })
})
