import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  docsSections,
  endpointGroups,
  httpEndpoints,
  publicEndpoints,
  sandboxLayers,
  serverBase,
  serverBaseLines,
  sessionOnlyEndpoints,
  sandboxNetworkModes,
  sandboxPolicyKinds,
  sandboxPolicySample,
} from '../src/lib/docs-content.js'
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

  it('resolves the resource policy and network-mode prose in every language', () => {
    for (const lang of LANGS) {
      const dict = resolve(lang)
      for (const key of [
        'sandboxPolicyTitle',
        'sandboxPolicyBody',
        'sandboxPolicyNote',
        'sandboxNetworkTitle',
        'colSandboxMode',
      ]) {
        expect(dict[key], `${lang}.${key} missing`).toBeTruthy()
      }
      for (const mode of sandboxNetworkModes) {
        expect(dict[mode.descKey], `${lang}.${mode.descKey} missing`).toBeTruthy()
      }
    }
  })

  it('lists the network modes from "no network" outwards', () => {
    // Order is part of the contract: the console renders them in this order, and
    // the reader must meet the default before any mode that opens a path.
    expect(sandboxNetworkModes.map((mode) => mode.mode)).toEqual([
      'none',
      'egress_allowlist',
      'bridge',
      'platform',
    ])
  })

  it('keeps egress_allowlist documented as executed like none', () => {
    // The value stays in the vocabulary so old records parse, but no provider
    // filters hosts. If this sentence ever loses the "none" fact, the docs would
    // be advertising a boundary the code does not draw.
    const allowlist = sandboxNetworkModes.find((mode) => mode.mode === 'egress_allowlist')
    expect(docsCopy.en[allowlist.descKey]).toContain('none')
    expect(docsCopy.en.sandboxPolicyNote).toContain('none')
  })

  it('documents the policy block only for the kinds that execute', () => {
    expect(sandboxPolicyKinds).toEqual(['agent', 'mcp', 'cli'])
  })

  it('shows a policy sample whose keys are the wire names', () => {
    // The sample is the shape the register drawer writes and the server parses.
    // camelCase here would document a block ValidateKind cannot read.
    const sample = JSON.parse(sandboxPolicySample)
    const policy = sample.kind_config.sandbox
    expect(Object.keys(policy).length).toBeGreaterThan(0)
    for (const key of Object.keys(policy)) {
      expect(key, `${key} is not snake_case`).toMatch(/^[a-z][a-z0-9_]*$/)
    }
    expect(policy.network).toBe('none')
    expect(sandboxPolicyKinds).toContain(sample.kind)
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

  it('keeps the whole route table in the docs screen', () => {
    // 144 is the route registry's count. The pin is deliberate: the sandbox block
    // (15 routes) and the router block used to be absent from this table even
    // though the handlers were live, and a count makes the next route land here
    // instead of only in the Go source.
    expect(httpEndpoints.length).toBe(144)
    const counts = {}
    for (const endpoint of httpEndpoints) {
      counts[endpoint.group] = (counts[endpoint.group] || 0) + 1
    }
    expect(counts.sandbox).toBe(15)
    expect(counts.router).toBe(14)
    expect(counts.files).toBe(5)
    expect(counts.collaboration).toBe(25)
    expect(counts.capabilities).toBe(10)
  })

  it('uses the wire field names in the register request sample', () => {
    const create = httpEndpoints.find((e) => e.method === 'POST' && e.path === '/api/resources')
    expect(create.request).toContain('display_name?')
    expect(create.request).toContain('kind_config?')
    expect(create.request).not.toContain('kindConfig')
  })

  it('says what a checkpoint does not contain, in every language', () => {
    // The route table can say `/api/backups` exists; only prose can say the
    // ledger stays out of the archive and what a guarded table does. Both facts
    // are pinned because they are the behaviour, not the wording — lose one and
    // the screen documents a different tool.
    const backups = endpointGroups.find((group) => group.id === 'backups')
    expect(backups?.noteKey).toBe('groupBackupsNote')
    for (const lang of LANGS) {
      expect(resolve(lang)[backups.noteKey], `${lang}.${backups.noteKey} missing`).toBeTruthy()
    }
    expect(docsCopy.en.groupBackupsNote).toContain('audit ledger')
    expect(docsCopy.en.groupBackupsNote).toContain('backup_table_guarded')
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

  it('derives the public / session classes from the Go route registry', () => {
    // Marking a requireAuth route `public`, or an anonymous discovery route
    // `session`, documents an auth boundary that does not exist. The two lists
    // are checked against the wrappers in handlers.go, not against themselves.
    const raw = readFileSync(
      fileURLToPath(new URL('../../internal/handlers/handlers.go', import.meta.url)),
      'utf8',
    )
    const publicFromGo = []
    const sessionFromGo = []
    for (const line of raw.split(/\r?\n/)) {
      const match = line.match(/HandleFunc\("([A-Z]+) (\/[^" ]+)",\s*(.+)\)\s*$/)
      if (!match) continue
      const [, method, path, wrapper] = match
      const key = `${method} ${path}`
      if (/requirePermission\(authz\.Perm/.test(wrapper)) continue
      if (/requireAuth\(/.test(wrapper)) sessionFromGo.push(key)
      else publicFromGo.push(key)
    }
    expect(publicFromGo.length).toBeGreaterThan(0)
    expect(sessionFromGo.length).toBeGreaterThan(0)
    expect([...publicEndpoints].sort()).toEqual(publicFromGo.sort())
    expect([...sessionOnlyEndpoints].sort()).toEqual(sessionFromGo.sort())
  })

  it('classifies every endpoint that has no permission code', () => {
    const classified = [...publicEndpoints, ...sessionOnlyEndpoints]
    expect(new Set(classified).size, 'a route is classified twice').toBe(classified.length)
    const blank = httpEndpoints
      .filter((endpoint) => !endpoint.permission)
      .map((endpoint) => `${endpoint.method} ${endpoint.path}`)
      .sort()
    expect([...classified].sort()).toEqual(blank)
  })

  it('pins the documented base URL to the server defaults', () => {
    // A port in prose goes stale the moment config.go or compose changes. The
    // numbers live in docs-content.js and are checked against both sources.
    const read = (path) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
    const defaultPort = read('../../internal/config/config.go').match(
      /envStr\("AGENTHUB_HTTP_PORT",\s*"(\d+)"\)/,
    )?.[1]
    const dbPort = read('../../docker-compose.yml').match(/\$\{POSTGRES_PORT:-(\d+)\}/)?.[1]
    expect(defaultPort, 'config.go default port not found').toBeTruthy()
    expect(dbPort, 'compose default database port not found').toBeTruthy()
    expect(serverBase.port).toBe(defaultPort)
    expect(serverBase.dbPort).toBe(dbPort)

    const sample = serverBaseLines.join('\n')
    for (const value of [
      serverBase.port,
      serverBase.portEnv,
      serverBase.dbPort,
      serverBase.dbPortEnv,
    ]) {
      expect(sample, `base URL sample is missing ${value}`).toContain(value)
    }
    for (const lang of LANGS) {
      const dict = resolve(lang)
      expect(dict.baseUrlTitle, `${lang}.baseUrlTitle missing`).toBeTruthy()
      expect(dict.baseUrlBody, `${lang}.baseUrlBody missing`).toBeTruthy()
    }
  })

  it('keeps the approval gate on the server and out of the playground', () => {
    // The route table documents the switch, and the view consumes the answer
    // as-is: a client-side status filter would re-admit a record the server
    // deliberately withheld from the discoverable catalogue.
    const listing = httpEndpoints.find((e) => e.method === 'GET' && e.path === '/api/resources')
    expect(listing?.request).toContain('discoverable=')

    const view = readFileSync(
      fileURLToPath(new URL('../src/views/PlaygroundView.vue', import.meta.url)),
      'utf8',
    )
    expect(view).toContain('store.discoverableResources')
    expect(view).toContain('refreshDiscoverableResources')
    expect(view).not.toContain('approvedResources')
    expect(view).not.toContain("status === 'approved'")
  })
})
