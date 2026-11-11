import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { publicConfig } from '../e2e.config.js'
import { redact } from './assertions.js'

const pad = (value, width) => String(value).padEnd(width)

/** Console output plus a JSON and a Markdown report in e2e/reports/. */
export function createReporter(config, suite = 'api') {
  const entries = []
  const cleanupFailures = []
  const startedAt = new Date()
  let currentGroup = 'general'

  function push(status, id, detail, durationMs) {
    const entry = { group: currentGroup, id, status, detail: redact(detail || ''), durationMs: durationMs ?? null }
    entries.push(entry)
    if (status === 'PASS') {
      process.stdout.write(`  PASS  ${id}\n`)
    } else if (status === 'SKIP') {
      process.stdout.write(`  SKIP  ${id} (${entry.detail})\n`)
    } else {
      process.stdout.write(`  FAIL  ${id} — ${entry.detail}\n`)
    }
  }

  return {
    group(name) {
      currentGroup = name
      process.stdout.write(`\n== ${name} ==\n`)
    },
    note(text) {
      process.stdout.write(`  ..    ${redact(text)}\n`)
    },
    pass(id, durationMs) {
      push('PASS', id, '', durationMs)
    },
    fail(id, error, durationMs) {
      push('FAIL', id, error?.message || String(error), durationMs)
    },
    skip(id, reason) {
      push('SKIP', id, reason, null)
    },
    cleanupFailure(label, error) {
      cleanupFailures.push({ label, message: redact(error?.message || String(error)) })
    },
    summary() {
      const pass = entries.filter((e) => e.status === 'PASS').length
      const fail = entries.filter((e) => e.status === 'FAIL').length
      const skip = entries.filter((e) => e.status === 'SKIP').length
      const byGroup = {}
      for (const entry of entries) {
        byGroup[entry.group] ||= { pass: 0, fail: 0, skip: 0 }
        byGroup[entry.group][entry.status.toLowerCase()] += 1
      }
      return {
        suite, startedAt: startedAt.toISOString(), finishedAt: new Date().toISOString(),
        pass, fail, skip, total: entries.length, byGroup,
        cleanupFailures, config: publicConfig(config),
      }
    },
    printSummary() {
      const s = this.summary()
      process.stdout.write(`\n${s.pass} passed, ${s.fail} failed, ${s.skip} skipped`)
      if (s.cleanupFailures.length) process.stdout.write(`, ${s.cleanupFailures.length} cleanup warning(s)`)
      process.stdout.write('\n')
      return s
    },
    async write() {
      const summary = this.summary()
      await mkdir(config.reportsDir, { recursive: true })
      const base = path.join(config.reportsDir, suite)
      await writeFile(`${base}.json`, JSON.stringify({ ...summary, entries }, null, 2) + '\n', 'utf8')
      const lines = [
        `# agenthub ${suite} E2E report`,
        '',
        `- started: ${summary.startedAt}`,
        `- finished: ${summary.finishedAt}`,
        `- target: \`${summary.config.baseURL}\``,
        `- auth mode: \`${summary.config.authMode}\``,
        `- result: **${summary.pass} passed / ${summary.fail} failed / ${summary.skip} skipped**`,
        '',
        '## Opt-in switches',
        '',
        '| switch | enabled |',
        '| --- | --- |',
        ...Object.entries(summary.config.optin).map(([k, v]) => `| \`${k}\` | ${v ? 'yes' : 'no (default)'} |`),
        '',
        '## Results',
        '',
        '| group | case | result | detail |',
        '| --- | --- | --- | --- |',
        ...entries.map((e) => `| ${e.group} | \`${e.id}\` | ${e.status} | ${escapeCell(e.detail)} |`),
        '',
      ]
      if (summary.cleanupFailures.length) {
        lines.push('## Cleanup warnings', '', ...summary.cleanupFailures.map((f) => `- \`${f.label}\`: ${escapeCell(f.message)}`), '')
      }
      lines.push(
        '## Notes', '',
        '- SKIP means the capability is not enabled on this instance, or an opt-in switch is off.',
        '- Secrets (password, session cookie, API key, signature) are redacted from every line.',
        '- Reports in this directory are gitignored.',
        '',
      )
      await writeFile(`${base}.md`, lines.join('\n'), 'utf8')
      return { json: `${base}.json`, markdown: `${base}.md` }
    },
  }
}

function escapeCell(text) {
  return String(text || '').replace(/\|/g, '\\|').replace(/\r?\n/g, ' ')
}
