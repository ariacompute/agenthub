import { describe, expect, it } from 'vitest'
import { formatBytes, formatDuration, formatRows, formatTime } from '../src/lib/format.js'

// The console renders measurements an operator reads before deciding whether a
// checkpoint is the one they want. The rule behind every helper is the same: a
// value the backend did not provide is an em dash, never a zero that could be
// mistaken for a measurement.

describe('formatBytes', () => {
  it('keeps bytes whole and steps through the units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(1024 * 1024 * 2.5)).toBe('2.5 MB')
    expect(formatBytes(1024 ** 4)).toBe('1.0 TB')
  })

  it('never invents a measurement', () => {
    expect(formatBytes(undefined)).toBe('0 B')
    expect(formatBytes(null)).toBe('0 B')
    expect(formatBytes(-1)).toBe('0 B')
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes('not a number')).toBe('0 B')
  })

  it('stops at the largest unit instead of inventing one', () => {
    expect(formatBytes(1024 ** 6)).toBe('1048576 TB')
  })
})

describe('formatDuration', () => {
  it('switches from milliseconds to seconds at one second', () => {
    expect(formatDuration(0)).toBe('0 ms')
    expect(formatDuration(1)).toBe('1 ms')
    expect(formatDuration(999)).toBe('999 ms')
    expect(formatDuration(1000)).toBe('1.0 s')
    expect(formatDuration(1480)).toBe('1.5 s')
    expect(formatDuration(92000)).toBe('92.0 s')
  })

  it('renders an unusable value as an em dash', () => {
    expect(formatDuration(-1)).toBe('—')
    expect(formatDuration(Number.NaN)).toBe('—')
    expect(formatDuration(undefined)).toBe('—')
  })
})

describe('formatRows', () => {
  it('groups thousands for copying out of the console', () => {
    expect(formatRows(42)).toBe('42')
    expect(formatRows(1234567)).toBe('1,234,567')
    expect(formatRows(0)).toBe('0')
  })

  it('renders an unusable value as an em dash', () => {
    expect(formatRows(undefined)).toBe('—')
    expect(formatRows('many')).toBe('—')
  })
})

describe('formatTime', () => {
  it('renders a real timestamp', () => {
    const out = formatTime('2026-10-18T06:37:00Z')
    expect(out).not.toBe('—')
    expect(out).toMatch(/2026/)
  })

  it('renders an unusable value as an em dash', () => {
    expect(formatTime('')).toBe('—')
    expect(formatTime(null)).toBe('—')
    expect(formatTime(undefined)).toBe('—')
    expect(formatTime('not a date')).toBe('—')
  })
})
