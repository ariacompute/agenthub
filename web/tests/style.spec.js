import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = new URL('../src/', import.meta.url).pathname

const HUE_WORDS = [
  'blue',
  'purple',
  'violet',
  'cyan',
  'teal',
  'green',
  'yellow',
  'red',
  'orange',
  'pink',
  'magenta',
  'indigo',
  'lime',
  'amber',
  'turquoise',
  'navy',
  'maroon',
  'olive',
  'crimson',
  'salmon',
  'gold',
  'bronze',
]

function walk(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      out.push(...walk(path))
    } else if (/\.(vue|js|css)$/.test(entry)) {
      out.push(path)
    }
  }
  return out
}

const FILES = walk(SRC)

function hexSamples(source) {
  const samples = []
  const re = /#([0-9a-fA-F]{3,8})\b/g
  let match
  while ((match = re.exec(source))) {
    samples.push(match[1])
  }
  return samples
}

function channelsOf(hex) {
  if (hex.length === 3 || hex.length === 4) {
    return hex.slice(0, 3).split('').map((c) => parseInt(c + c, 16))
  }
  return [hex.slice(0, 2), hex.slice(2, 4), hex.slice(4, 6)].map((c) => parseInt(c, 16))
}

describe('monochrome guard', () => {
  it('scans the whole src tree', () => {
    expect(FILES.length).toBeGreaterThan(8)
  })

  it('uses only grey hex colours (R === G === B)', () => {
    for (const file of FILES) {
      for (const hex of hexSamples(readFileSync(file, 'utf8'))) {
        const [r, g, b] = channelsOf(hex)
        expect(r, `${file}: #${hex} is not grey`).toBe(g)
        expect(g, `${file}: #${hex} is not grey`).toBe(b)
      }
    }
  })

  it('uses only neutral rgb()/rgba()/hsl() channels', () => {
    const re = /(?:rgba?|hsla?)\(([^)]*)\)/g
    for (const file of FILES) {
      const source = readFileSync(file, 'utf8')
      let match
      while ((match = re.exec(source))) {
        const parts = match[1].split(',').map((part) => parseFloat(part))
        if (parts.length < 3 || parts.slice(0, 3).some(Number.isNaN)) continue
        const [a, b, c] = parts.slice(0, 3)
        const neutral = a === b && b === c
        const monochromeEnds = (a === 0 || a === 255) && (b === 0 || b === 255) && (c === 0 || c === 255)
        expect(neutral || monochromeEnds, `${file}: ${match[0]} is not neutral`).toBe(true)
      }
    }
  })

  it('never mentions a hue word', () => {
    for (const file of FILES) {
      const source = readFileSync(file, 'utf8').toLowerCase()
      for (const word of HUE_WORDS) {
        expect(new RegExp(`\\b${word}\\b`).test(source), `${file} mentions "${word}"`).toBe(false)
      }
    }
  })
})
