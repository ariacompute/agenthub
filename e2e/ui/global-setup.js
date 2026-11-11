import { ensureStorageState } from '../lib/auth.js'

/** Login once for the whole UI run; the API suite writes the same state file. */
export default async function globalSetup() {
  const result = await ensureStorageState()
  process.stdout.write(`[e2e] storageState ${result.reused ? 'reused' : 'written'} (${result.mode || 'login'})\n`)
}
