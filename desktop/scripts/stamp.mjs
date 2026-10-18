// Writes the release version into the two places the desktop build reads it
// from, so an installer's version is always the tag it was published under.
//
// Used by the release workflow only; a local build keeps the version in the
// repository. Kept as a script rather than a sed one-liner because it has to
// behave identically on the Linux, macOS and Windows runners.
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const version = process.argv[2]
if (!version || !/^[0-9]+\.[0-9]+\.[0-9]+.*$/.test(version)) {
  console.error(`usage: stamp.mjs <semver version>   (got: ${version ?? 'nothing'})`)
  process.exit(1)
}

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const targets = [
  // The first `version = "…"` in Cargo.toml is the package's own; the rest are
  // dependency requirements and must not be touched.
  [join(root, 'src-tauri', 'Cargo.toml'), /^version = "[^"]*"/m, `version = "${version}"`],
  [join(root, 'src-tauri', 'tauri.conf.json'), /"version": "[^"]*"/, `"version": "${version}"`],
]

for (const [path, pattern, replacement] of targets) {
  const before = readFileSync(path, 'utf8')
  const after = before.replace(pattern, replacement)
  if (before === after) {
    console.error(`stamp: no version to replace in ${path}`)
    process.exit(1)
  }
  writeFileSync(path, after)
}

console.log(`stamped ${version}`)
