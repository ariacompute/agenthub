/**
 * The single aggregation point for the declarative case table. Order matters:
 * earlier groups create the objects later groups consume (resource -> task ->
 * spans/events -> admin grants), and the opt-in group runs last because a real
 * logout invalidates the shared session.
 */
import c00 from './00-health-discovery.js'
import c01 from './01-auth-session.js'
import c02 from './02-resources.js'
import c03 from './03-governance.js'
import c04 from './04-a2a.js'
import c05 from './05-stream-events.js'
import c06 from './06-tasks.js'
import c07 from './07-admin-rbac.js'
import c08 from './08-channels-connectors.js'
import c09 from './09-threads-speech.js'
import c10 from './10-files.js'
import c11 from './11-sandboxes.js'
import c12 from './12-schedules-computers-knowledge.js'
import c13 from './13-router.js'
import c14 from './14-audit.js'
import c15 from './15-backups.js'
import c16 from './16-error-contract.js'
import c99 from './99-optin.js'

export const ALL_CASES = [
  c00, c01, c02, c03, c04, c05, c06, c07,
  c08, c09, c10, c11, c12, c13, c14, c15,
  c16, c99,
].flat()
