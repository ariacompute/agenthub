import { describe, it, expect } from 'vitest'
import { docsSections, endpointGroups, httpEndpoints, sandboxLayers } from '../src/lib/docs-content.js'
import { docsCopy } from '../src/lib/copy/docs-copy.js'

// The /docs screen renders keys, not prose: a key with no translation renders its
// own name, which is how a section goes missing in one language only. The
// markdown in docs/docs*.md is generated from the same two modules, so a hole
// here would also land in the files an operator reads offline.

const LANGS = ['en', 'zh', 'zh-TW', 'es', 'fr', 'de', 'it', 'ru', 'ja', 'ko', 'pt', 'ar', 'hi']

const resolve = (lang) => ({ ...docsCopy.en, ...(docsCopy[lang] || {}) })

describe('docs content', () => {
  it('resolves every section title and body in every language', () => {
    for (const lang of LANGS) {
      const dict = resolve(lang)
      for (const section of docsSections) {
        expect(dict[section.titleKey], `${lang}.${section.titleKey} missing`).toBeTruthy()
        expect(dict[section.bodyKey], `${lang}.${section.bodyKey} missing`).toBeTruthy()
      }
    }
  })

  it('resolves every sandbox layer description in every language', () => {
    expect(sandboxLayers.length).toBeGreaterThan(0)
    for (const lang of LANGS) {
      const dict = resolve(lang)
      for (const entry of sandboxLayers) {
        expect(dict[entry.descKey], `${lang}.${entry.descKey} missing`).toBeTruthy()
      }
    }
  })

  it('lists the execution layers from the default boundary outwards', () => {
    // Presentation order is part of the contract: the console groups by the same
    // order, and the default must be the first row an operator reads.
    expect(sandboxLayers[0].layer).toBe('container')
    expect(sandboxLayers.at(-1).layer).toBe('none')
    const declared = sandboxLayers.map((entry) => entry.layer)
    expect(new Set(declared).size).toBe(declared.length)
  })

  it('keeps the sandbox section among the rendered sections', () => {
    expect(docsSections.map((section) => section.id)).toContain('sandbox')
  })

  it('resolves every endpoint description and noun in every language', () => {
    // An endpoint whose descKey is missing renders its own key as prose, which is
    // how a route silently drops out of the table. The noun is what `{n}` becomes.
    for (const lang of LANGS) {
      const dict = resolve(lang)
      for (const endpoint of httpEndpoints) {
        expect(dict[endpoint.descKey], `${lang}.${endpoint.descKey} missing`).toBeTruthy()
        if (endpoint.nounKey) {
          expect(dict[endpoint.nounKey], `${lang}.${endpoint.nounKey} missing`).toBeTruthy()
        }
      }
    }
  })

  it('files every endpoint under a group the table renders', () => {
    const declared = endpointGroups.map((group) => group.id)
    expect(new Set(declared).size).toBe(declared.length)
    for (const group of endpointGroups) {
      expect(docsCopy.en[group.groupKey], `group ${group.id} has no title`).toBeTruthy()
      expect(
        httpEndpoints.some((endpoint) => endpoint.group === group.id),
        `group ${group.id} declares no endpoint`,
      ).toBe(true)
    }
    for (const endpoint of httpEndpoints) {
      expect(declared, `${endpoint.method} ${endpoint.path} has no group`).toContain(endpoint.group)
    }
  })
})
