// Presentation helpers for the console.
//
// Everything here is pure: no locale is invented, no unit is guessed. A value
// the backend does not provide is rendered as an em dash rather than as "0",
// which would read as a measurement.

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

/** Human readable size. Bytes stay whole numbers: a checkpoint is a file. */
export function formatBytes(bytes) {
  const value = Number(bytes)
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  let size = value
  let unit = 0
  while (size >= 1024 && unit < UNITS.length - 1) {
    size /= 1024
    unit += 1
  }
  const digits = size < 10 && unit > 0 ? 1 : 0
  return `${size.toFixed(digits)} ${UNITS[unit]}`
}

/** "820 ms" under a second, "1.4 s" above it: a dump is measured in both. */
export function formatDuration(ms) {
  const value = Number(ms)
  if (!Number.isFinite(value) || value < 0) return '—'
  if (value < 1000) return `${Math.round(value)} ms`
  return `${(value / 1000).toFixed(1)} s`
}

/** Thousands separators for row counts copied out of the console. */
export function formatRows(rows) {
  const value = Number(rows)
  if (!Number.isFinite(value)) return '—'
  return value.toLocaleString('en-US')
}

// The console formats timestamps in the browser's locale, because a checkpoint
// is read by an operator sitting in a timezone — not by the server that made
// it. An unparseable value stays an em dash instead of "Invalid Date".
export function formatTime(value) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}
