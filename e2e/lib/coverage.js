import { existsSync, readFileSync } from 'node:fs'

/**
 * Parse the generated endpoint tables without modifying them. The Go suite
 * (tests/docs_endpoints_test.go) already guards handlers.go <-> docs both ways;
 * this guard closes the remaining gap: docs <-> our declared cases.
 *
 * Only the first two cells matter, so the `\|` escapes used inside later cells
 * (JSON unions like `"user"\|"group"`) cannot confuse the parser.
 */
export function parseDocsEndpoints(paths) {
  const byRoute = new Map()
  for (const file of paths) {
    if (!existsSync(file)) continue
    let group = 'ungrouped'
    for (const line of readFileSync(file, 'utf8').split(/\r?\n/)) {
      const heading = line.match(/^###\s+(.+?)\s*$/)
      if (heading) {
        group = heading[1].trim()
        continue
      }
      const match = line.match(/^\|\s*`([A-Z]+)`\s*\|\s*`(\/[^`]+)`/)
      if (!match) continue
      const route = `${match[1]} ${match[2]}`
      if (!byRoute.has(route)) byRoute.set(route, group)
    }
  }
  return byRoute
}

/**
 * Compare declared cases against the docs. `missing` fails a strict run;
 * `extra` means a case points at a route the docs do not describe (usually a
 * typo), which is equally worth catching.
 */
export function checkCoverage(cases, docsByRoute) {
  const declared = new Set(cases.map((c) => c.route).filter(Boolean))
  const missing = []
  for (const [route, group] of docsByRoute) {
    if (!declared.has(route)) missing.push({ route, group })
  }
  const extra = [...declared].filter((route) => !docsByRoute.has(route)).sort()
  return {
    total: docsByRoute.size,
    declared: declared.size,
    covered: docsByRoute.size - missing.length,
    missing: missing.sort((a, b) => a.route.localeCompare(b.route)),
    extra,
  }
}

export function printCoverage(result) {
  process.stdout.write(`\n== endpoint coverage ==\n`)
  process.stdout.write(`  ${result.covered}/${result.total} documented routes have a case (${result.declared} declared)\n`)
  if (result.missing.length) {
    process.stdout.write('  missing:\n')
    for (const { route, group } of result.missing) {
      process.stdout.write(`    ${pad(route, 58)} [${group}]\n`)
    }
  }
  if (result.extra.length) {
    process.stdout.write('  undeclared in docs:\n')
    for (const route of result.extra) process.stdout.write(`    ${route}\n`)
  }
}

const pad = (value, width) => String(value).padEnd(width)
