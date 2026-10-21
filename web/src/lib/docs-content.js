// Documentation skeleton for the console's own docs screen.
//
// Everything here is language-neutral: HTTP methods, paths, permission codes,
// field names and JSON samples are machine data and render verbatim in every
// locale, exactly like the build note in the sidebar. Only the `descKey` and
// `nounKey` pointers cross over into docs-copy.js, so the translated tables
// stay prose and nothing else.
//
// The endpoint list mirrors internal/handlers/handlers.go. When a route moves
// there it moves here too: this file is the user-facing half of that table.

/** The five top-level sections, in reading order. */
export const docsSections = [
  { id: 'concepts', titleKey: 'sectionConcepts', bodyKey: 'conceptsBody' },
  { id: 'a2a', titleKey: 'sectionA2a', bodyKey: 'a2aBody' },
  { id: 'http', titleKey: 'sectionHttp', bodyKey: 'httpBody' },
  { id: 'auth', titleKey: 'sectionAuth', bodyKey: 'authBody' },
  { id: 'errors', titleKey: 'sectionErrors', bodyKey: 'errorsBody' },
]

/** Six resource lanes. `kind` renders through enumLabel, never translated here. */
export const resourceKinds = [
  { kind: 'model', descKey: 'kindModelDesc' },
  { kind: 'agent', descKey: 'kindAgentDesc' },
  { kind: 'mcp', descKey: 'kindMcpDesc' },
  { kind: 'skill', descKey: 'kindSkillDesc' },
  { kind: 'cli', descKey: 'kindCliDesc' },
  { kind: 'custom', descKey: 'kindCustomDesc' },
]

/** Task lifecycle, rendered as one flow. */
export const taskStates = ['submitted', 'working', 'input-required', 'completed', 'failed', 'canceled']

/** Health values; the legend explains how each one is earned. */
export const healthStates = ['healthy', 'degraded', 'unhealthy', 'unknown']

/** Curation lifecycle of a registry record. */
export const recordStatuses = ['draft', 'pending', 'approved', 'rejected', 'deprecated']

/**
 * A2A methods. `alias` is the v0.1 name accepted when legacy methods are on and
 * is shown as a footnote rather than as its own row.
 */
export const a2aMethods = [
  {
    method: 'message/send',
    descKey: 'mSend',
    params: '{ message, configuration?, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'message/stream',
    descKey: 'mStream',
    params: '{ message, configuration?, metadata? }',
    response: 'text/event-stream: task_status_update | task_artifact_update | message | task_span_update',
  },
  {
    method: 'tasks/get',
    descKey: 'mTasksGet',
    params: '{ id, historyLength?, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/cancel',
    descKey: 'mTasksCancel',
    params: '{ id, historyLength?, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/resume',
    descKey: 'mTasksResume',
    params: '{ id, message, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/pushNotification/set',
    descKey: 'mPushSet',
    params: '{ taskId, pushNotificationConfig? }',
    response: '{ jsonrpc: "2.0", id, result: PushNotificationConfig }',
  },
  {
    method: 'tasks/pushNotification/get',
    descKey: 'mPushGet',
    params: '{ id }',
    response: '{ jsonrpc: "2.0", id, result: PushNotificationConfig }',
  },
]

/** v0.1 aliases, accepted when AGENTHUB_A2A_LEGACY_METHODS is enabled. */
export const a2aAliases = [
  { alias: 'tasks/send', method: 'message/send' },
  { alias: 'tasks/sendSubscribe', method: 'message/stream' },
]

/** The two shapes every A2A call moves: what it takes and what it returns. */
export const a2aShapes = [
  {
    title: 'Message',
    body: '{\n  "kind": "message",\n  "role": "user",\n  "messageId": "…",\n  "parts": [\n    { "kind": "text", "type": "text", "text": "probe google-adk" }\n  ]\n}',
  },
  {
    title: 'Task',
    body: '{\n  "kind": "task",\n  "id": "task-ff5eba3cefe2be87",\n  "status": {\n    "state": "completed",\n    "message": { "role": "agent", "parts": [{ "type": "text", "text": "…" }] }\n  },\n  "artifacts": [\n    { "artifactId": "agent.a2a-0", "name": "agent.a2a", "parts": [{ "type": "data", "data": { … } }] }\n  ],\n  "history": [{ "role": "user", "parts": [{ "type": "text", "text": "…" }] }]\n}',
  },
]

/** Endpoint groups, in the order the route table registers them. */
export const endpointGroups = [
  { id: 'protocol', groupKey: 'groupProtocol' },
  { id: 'auth', groupKey: 'groupAuth' },
  { id: 'resources', groupKey: 'groupResources' },
  { id: 'registries', groupKey: 'groupRegistries' },
  { id: 'sources', groupKey: 'groupSources' },
  { id: 'tasks', groupKey: 'groupTasks' },
  { id: 'export', groupKey: 'groupExport' },
  { id: 'users', groupKey: 'groupUsers' },
  { id: 'roles', groupKey: 'groupRoles' },
  { id: 'backups', groupKey: 'groupBackups' },
  { id: 'apikeys', groupKey: 'groupApiKeys' },
  { id: 'ops', groupKey: 'groupOps' },
]

/**
 * Every route the single listener serves.
 *
 * `permission` is the authz code the route demands and is rendered verbatim
 * (its label comes from enumLabel('permission', …)). `descKey` names a prose
 * key in docs-copy.js; when `nounKey` is set the key is a template and the noun
 * is substituted into it, which keeps CRUD rows from needing 60 near-identical
 * translations.
 */
export const httpEndpoints = [
  // ---- protocol ----
  {
    group: 'protocol',
    method: 'GET',
    path: '/.well-known/agent.json',
    permission: '',
    descKey: 'epAgentCard',
    request: '',
    response: 'AgentCard { name, description, url, version, protocolVersion, capabilities, skills[] }',
  },
  {
    group: 'protocol',
    method: 'GET',
    path: '/a2a/agent-card',
    permission: '',
    descKey: 'epAgentCardAlt',
    request: '',
    response: 'AgentCard',
  },
  {
    group: 'protocol',
    method: 'POST',
    path: '/a2a',
    permission: '',
    descKey: 'epA2aRpc',
    request: '{ jsonrpc: "2.0", id, method: "message/send", params }',
    response: '{ jsonrpc: "2.0", id, result } | { jsonrpc: "2.0", id, error: { code, message } }',
  },
  {
    group: 'protocol',
    method: 'GET',
    path: '/a2a/stream',
    permission: 'tasks:read',
    descKey: 'epA2aStream',
    request: '?task=<id>&since=<seq>',
    response: 'text/event-stream, keep-alive every 15s',
  },

  // ---- auth ----
  {
    group: 'auth',
    method: 'GET',
    path: '/api/auth/config',
    permission: '',
    descKey: 'epAuthConfig',
    request: '',
    response: '{ providers: [{ name }], password, registrationOpen, configured }',
  },
  {
    group: 'auth',
    method: 'POST',
    path: '/api/auth/register',
    permission: '',
    descKey: 'epAuthRegister',
    request: '{ email, password, name }',
    response: '{ user } — 201',
  },
  {
    group: 'auth',
    method: 'POST',
    path: '/api/auth/login',
    permission: '',
    descKey: 'epAuthLogin',
    request: '{ email, password }',
    response: '{ user } and a session cookie',
  },
  {
    group: 'auth',
    method: 'POST',
    path: '/api/auth/logout',
    permission: '',
    descKey: 'epAuthLogout',
    request: '',
    response: '204, no body',
  },
  {
    group: 'auth',
    method: 'GET',
    path: '/api/auth/me',
    permission: '',
    descKey: 'epAuthMe',
    request: '',
    response: '{ user: { id, email, name, isOwner, roles, groups }, permissions: [] }',
  },
  {
    group: 'auth',
    method: 'GET',
    path: '/api/auth/sessions',
    permission: '',
    descKey: 'verbList',
    nounKey: 'nounSessions',
    request: '',
    response: '{ sessions: [{ id, createdAt, expiresAt, lastSeenAt, current }] }',
  },
  {
    group: 'auth',
    method: 'DELETE',
    path: '/api/auth/sessions/{id}',
    permission: '',
    descKey: 'verbDelete',
    nounKey: 'nounSessions',
    request: '',
    response: '204, no body',
  },
  {
    group: 'auth',
    method: 'GET',
    path: '/api/auth/oauth/{provider}',
    permission: '',
    descKey: 'epOAuthStart',
    request: '',
    response: '302 to the provider, PKCE + state',
  },
  {
    group: 'auth',
    method: 'GET',
    path: '/api/auth/oauth/{provider}/callback',
    permission: '',
    descKey: 'epOAuthCallback',
    request: '',
    response: '302 to the console with a session cookie',
  },

  // ---- resources ----
  {
    group: 'resources',
    method: 'GET',
    path: '/api/resources',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounResources',
    request: '?kind=&q=&namespace=&status=',
    response: '{ resources: [Entry], counts: { kind }, health: { state } }',
  },
  {
    group: 'resources',
    method: 'POST',
    path: '/api/resources',
    permission: 'resources:write',
    descKey: 'verbCreate',
    nounKey: 'nounResources',
    request: '{ kind, name, endpoint?, transport?, namespace?, kindConfig? }',
    response: 'Entry — 201',
  },
  {
    group: 'resources',
    method: 'GET',
    path: '/api/resources/{id}',
    permission: 'resources:read',
    descKey: 'verbGet',
    nounKey: 'nounResources',
    request: '',
    response: 'Entry',
  },
  {
    group: 'resources',
    method: 'PUT',
    path: '/api/resources/{id}',
    permission: 'resources:write',
    descKey: 'verbReplace',
    nounKey: 'nounResources',
    request: 'Entry — the path id wins over the body',
    response: 'Entry',
  },
  {
    group: 'resources',
    method: 'DELETE',
    path: '/api/resources/{id}',
    permission: 'resources:write',
    descKey: 'verbDelete',
    nounKey: 'nounResources',
    request: '',
    response: '204, no body',
  },
  {
    group: 'resources',
    method: 'POST',
    path: '/api/resources/{id}/health',
    permission: 'resources:write',
    descKey: 'epHealthSet',
    request: '{ health }',
    response: '{ health }',
  },
  {
    group: 'resources',
    method: 'POST',
    path: '/api/resources/{id}/probe',
    permission: 'resources:write',
    descKey: 'epProbe',
    request: '',
    response: '{ id, health, probed, reason? }',
  },
  {
    group: 'resources',
    method: 'POST',
    path: '/api/resources/{id}/status',
    permission: 'registry:curate',
    descKey: 'epStatusSet',
    request: '{ status, reason? }',
    response: 'Entry',
  },

  // ---- registries ----
  {
    group: 'registries',
    method: 'GET',
    path: '/api/registries',
    permission: 'registries:manage',
    descKey: 'verbList',
    nounKey: 'nounNamespaces',
    request: '',
    response: '{ registries: [{ id, name, description?, auto_approve }] }',
  },
  {
    group: 'registries',
    method: 'POST',
    path: '/api/registries',
    permission: 'registries:manage',
    descKey: 'verbCreate',
    nounKey: 'nounNamespaces',
    request: '{ id, name, description?, auto_approve }',
    response: 'Namespace — 201',
  },
  {
    group: 'registries',
    method: 'PUT',
    path: '/api/registries/{id}',
    permission: 'registries:manage',
    descKey: 'verbReplace',
    nounKey: 'nounNamespaces',
    request: 'Namespace',
    response: 'Namespace',
  },
  {
    group: 'registries',
    method: 'DELETE',
    path: '/api/registries/{id}',
    permission: 'registries:manage',
    descKey: 'verbDelete',
    nounKey: 'nounNamespaces',
    request: '',
    response: '204, no body',
  },
  {
    group: 'registries',
    method: 'GET',
    path: '/api/registries/approvals',
    permission: 'registry:curate',
    descKey: 'epApprovals',
    request: '',
    response: '{ records: [Entry], total }',
  },

  // ---- sources ----
  {
    group: 'sources',
    method: 'GET',
    path: '/api/registry-sources',
    permission: 'sources:manage',
    descKey: 'verbList',
    nounKey: 'nounSources',
    request: '?namespace=',
    response: '{ sources: [{ id, namespace, kind, name, url, enabled, last_sync_at? }] }',
  },
  {
    group: 'sources',
    method: 'POST',
    path: '/api/registry-sources',
    permission: 'sources:manage',
    descKey: 'verbCreate',
    nounKey: 'nounSources',
    request: '{ namespace, kind, name, url, auth_scheme?, enabled? }',
    response: 'RegistrySource — 201',
  },
  {
    group: 'sources',
    method: 'PUT',
    path: '/api/registry-sources/{id}',
    permission: 'sources:manage',
    descKey: 'verbReplace',
    nounKey: 'nounSources',
    request: 'RegistrySource',
    response: 'RegistrySource',
  },
  {
    group: 'sources',
    method: 'DELETE',
    path: '/api/registry-sources/{id}',
    permission: 'sources:manage',
    descKey: 'verbDelete',
    nounKey: 'nounSources',
    request: '',
    response: '204, no body',
  },
  {
    group: 'sources',
    method: 'POST',
    path: '/api/registry-sources/{id}/sync',
    permission: 'sources:manage',
    descKey: 'epSync',
    request: '',
    response: '{ status, imported, updated, skipped, total, error?, at }',
  },

  // ---- tasks ----
  {
    group: 'tasks',
    method: 'GET',
    path: '/api/tasks',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounTasks',
    request: '',
    response: '{ tasks: [Task] }',
  },
  {
    group: 'tasks',
    method: 'GET',
    path: '/api/tasks/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounTasks',
    request: '',
    response: 'Task',
  },
  {
    group: 'tasks',
    method: 'GET',
    path: '/api/tasks/{id}/spans',
    permission: 'tasks:read',
    descKey: 'epTaskSpans',
    request: '',
    response: '{ spans: [{ id, taskId, label, startAt, endAt?, durationMs, status }] }',
  },
  {
    group: 'tasks',
    method: 'GET',
    path: '/api/tasks/{id}/events',
    permission: 'tasks:read',
    descKey: 'epTaskEvents',
    request: '?since=<seq>',
    response: 'text/event-stream of { seq, taskId, kind, at, status? }',
  },
  {
    group: 'tasks',
    method: 'POST',
    path: '/api/tasks/{id}/cancel',
    permission: 'tasks:write',
    descKey: 'epTaskCancel',
    request: '',
    response: 'Task',
  },
  {
    group: 'tasks',
    method: 'POST',
    path: '/api/tasks/{id}/resume',
    permission: 'tasks:write',
    descKey: 'epTaskResume',
    request: '{ id, message, metadata? }',
    response: 'Task',
  },

  // ---- export ----
  {
    group: 'export',
    method: 'GET',
    path: '/api/export/mcp',
    permission: 'resources:read',
    descKey: 'epExportMcp',
    request: '',
    response: '{ servers: [{ name, endpoint, transport, capabilities, health }] }',
  },
  {
    group: 'export',
    method: 'GET',
    path: '/api/export/agent-card',
    permission: 'resources:read',
    descKey: 'epExportCard',
    request: '',
    response: 'AgentCard',
  },

  // ---- users ----
  {
    group: 'users',
    method: 'GET',
    path: '/api/users',
    permission: 'users:manage',
    descKey: 'verbList',
    nounKey: 'nounUsers',
    request: '?q=',
    response: '{ users: [{ id, email, name, status, isOwner, roles, groups }] }',
  },
  {
    group: 'users',
    method: 'POST',
    path: '/api/users',
    permission: 'users:manage',
    descKey: 'verbCreate',
    nounKey: 'nounUsers',
    request: '{ email, password, name, roles[], groups[] }',
    response: '{ user } — 201',
  },
  {
    group: 'users',
    method: 'GET',
    path: '/api/users/{id}',
    permission: 'users:manage',
    descKey: 'verbGet',
    nounKey: 'nounUsers',
    request: '',
    response: '{ user }',
  },
  {
    group: 'users',
    method: 'PUT',
    path: '/api/users/{id}',
    permission: 'users:manage',
    descKey: 'verbReplace',
    nounKey: 'nounUsers',
    request: '{ email?, name?, password?, status?, roles[]?, groups[]? }',
    response: '{ user }',
  },
  {
    group: 'users',
    method: 'DELETE',
    path: '/api/users/{id}',
    permission: 'users:manage',
    descKey: 'verbDelete',
    nounKey: 'nounUsers',
    request: '',
    response: '204, no body',
  },
  {
    group: 'users',
    method: 'DELETE',
    path: '/api/users/{id}/sessions',
    permission: 'sessions:manage',
    descKey: 'epRevokeUserSessions',
    request: '',
    response: '204, no body',
  },

  // ---- roles, groups, grants ----
  {
    group: 'roles',
    method: 'GET',
    path: '/api/permissions',
    permission: 'roles:manage',
    descKey: 'verbList',
    nounKey: 'nounPermissions',
    request: '',
    response: '{ permissions: [{ code, description }] }',
  },
  {
    group: 'roles',
    method: 'GET',
    path: '/api/roles',
    permission: 'roles:manage',
    descKey: 'verbList',
    nounKey: 'nounRoles',
    request: '',
    response: '{ roles: [{ id, name, description, builtin, permissions[] }] }',
  },
  {
    group: 'roles',
    method: 'POST',
    path: '/api/roles',
    permission: 'roles:manage',
    descKey: 'verbCreate',
    nounKey: 'nounRoles',
    request: '{ name, description?, permissions[] }',
    response: '{ role } — 201',
  },
  {
    group: 'roles',
    method: 'PUT',
    path: '/api/roles/{id}',
    permission: 'roles:manage',
    descKey: 'verbReplace',
    nounKey: 'nounRoles',
    request: 'Role',
    response: '{ role }',
  },
  {
    group: 'roles',
    method: 'DELETE',
    path: '/api/roles/{id}',
    permission: 'roles:manage',
    descKey: 'verbDelete',
    nounKey: 'nounRoles',
    request: '',
    response: '204, no body',
  },
  {
    group: 'roles',
    method: 'GET',
    path: '/api/groups',
    permission: 'roles:manage',
    descKey: 'verbList',
    nounKey: 'nounGroups',
    request: '',
    response: '{ groups: [{ id, name, description, roles[] }] }',
  },
  {
    group: 'roles',
    method: 'POST',
    path: '/api/groups',
    permission: 'roles:manage',
    descKey: 'verbCreate',
    nounKey: 'nounGroups',
    request: '{ name, description?, roles[] }',
    response: '{ group } — 201',
  },
  {
    group: 'roles',
    method: 'PUT',
    path: '/api/groups/{id}',
    permission: 'roles:manage',
    descKey: 'verbReplace',
    nounKey: 'nounGroups',
    request: 'Group',
    response: '{ group }',
  },
  {
    group: 'roles',
    method: 'DELETE',
    path: '/api/groups/{id}',
    permission: 'roles:manage',
    descKey: 'verbDelete',
    nounKey: 'nounGroups',
    request: '',
    response: '204, no body',
  },
  {
    group: 'roles',
    method: 'GET',
    path: '/api/grants',
    permission: 'grants:manage',
    descKey: 'verbList',
    nounKey: 'nounGrants',
    request: '?resource=',
    response: '{ grants: [{ id, resourceId, principalKind, principalId, level }] }',
  },
  {
    group: 'roles',
    method: 'POST',
    path: '/api/grants',
    permission: 'grants:manage',
    descKey: 'verbCreate',
    nounKey: 'nounGrants',
    request: '{ resourceId, principalKind: "user"|"group", principalId, level: "read"|"write" }',
    response: '{ grant } — 201',
  },
  {
    group: 'roles',
    method: 'DELETE',
    path: '/api/grants/{id}',
    permission: 'grants:manage',
    descKey: 'verbDelete',
    nounKey: 'nounGrants',
    request: '',
    response: '204, no body',
  },

  // ---- backups ----
  {
    group: 'backups',
    method: 'GET',
    path: '/api/backups',
    permission: 'backups:manage',
    descKey: 'epBackupsOverview',
    request: '',
    response: '{ enabled, reason?, engine?, nextRun?, activeTasks, checkpoints: [], keepLast, maxAgeDays }',
  },
  {
    group: 'backups',
    method: 'POST',
    path: '/api/backups',
    permission: 'backups:manage',
    descKey: 'epBackupCreate',
    request: '{ label? }',
    response: '{ checkpoint } — 201',
  },
  {
    group: 'backups',
    method: 'GET',
    path: '/api/backups/schedule',
    permission: 'backups:manage',
    descKey: 'epScheduleGet',
    request: '',
    response: '{ schedule: { mode, enabled, intervalSec, dailyTime, timezone, cronExpr, keepLast, maxAgeDays, nextRun? } }',
  },
  {
    group: 'backups',
    method: 'PUT',
    path: '/api/backups/schedule',
    permission: 'backups:manage',
    descKey: 'epSchedulePut',
    request: '{ mode, enabled, intervalSec, dailyTime, timezone, cronExpr, keepLast, maxAgeDays }',
    response: '{ schedule }',
  },
  {
    group: 'backups',
    method: 'GET',
    path: '/api/backups/{id}',
    permission: 'backups:manage',
    descKey: 'verbGet',
    nounKey: 'nounCheckpoints',
    request: '',
    response: '{ id, kind, engine, status, sizeBytes, rowCount, checksum, createdAt }',
  },
  {
    group: 'backups',
    method: 'DELETE',
    path: '/api/backups/{id}',
    permission: 'backups:manage',
    descKey: 'verbDelete',
    nounKey: 'nounCheckpoints',
    request: '',
    response: '204, no body',
  },
  {
    group: 'backups',
    method: 'GET',
    path: '/api/backups/{id}/download',
    permission: 'backups:manage',
    descKey: 'epBackupDownload',
    request: '',
    response: 'application/gzip',
  },
  {
    group: 'backups',
    method: 'POST',
    path: '/api/backups/{id}/restore',
    permission: 'backups:manage',
    descKey: 'epBackupRestore',
    request: '{ confirm } — must echo the checkpoint id',
    response: '{ checkpoint }',
  },

  // ---- api keys ----
  {
    group: 'apikeys',
    method: 'GET',
    path: '/api/api-keys',
    permission: '',
    descKey: 'epApiKeyList',
    request: '',
    response: '{ apiKeys: [{ id, name, prefix, scopes, createdAt, lastUsedAt, expiresAt, revoked }] }',
  },
  {
    group: 'apikeys',
    method: 'POST',
    path: '/api/api-keys',
    permission: '',
    descKey: 'epApiKeyCreate',
    request: '{ name, scopes[], userId?, expiresInDays? }',
    response: '{ apiKey, token } — 201, token shown once',
  },
  {
    group: 'apikeys',
    method: 'GET',
    path: '/api/api-keys/{id}',
    permission: '',
    descKey: 'epApiKeyGet',
    request: '',
    response: '{ apiKey }',
  },
  {
    group: 'apikeys',
    method: 'PUT',
    path: '/api/api-keys/{id}',
    permission: '',
    descKey: 'epApiKeyUpdate',
    request: '{ name?, scopes[], expiresAt? }',
    response: '{ apiKey }',
  },
  {
    group: 'apikeys',
    method: 'DELETE',
    path: '/api/api-keys/{id}',
    permission: '',
    descKey: 'epApiKeyRevoke',
    request: '',
    response: '204, no body',
  },

  // ---- ops ----
  {
    group: 'ops',
    method: 'GET',
    path: '/healthz',
    permission: '',
    descKey: 'epHealthz',
    request: '',
    response: '{ status, version, commit, buildNote, protocolVersion, resources, health, tasks, methods[], auth, database } — 503 when the database is down',
  },
]

/**
 * Error reporting: the body is always `{"code": "..."}` and the HTTP status is
 * chosen from the code.
 *
 * `reason` is the standard reason phrase for the status and is rendered
 * verbatim in every locale: it is part of the protocol, not prose. Grouping by
 * status also keeps the table honest without translating sixty code strings.
 */
export const errorGroups = [
  { status: '400', reason: 'Bad Request', codes: 'bad_request, weak_password, invalid_email, invalid_grant, unknown_permission, password_required, backup_schedule_invalid, restore_confirm_required' },
  { status: '401', reason: 'Unauthorized', codes: 'unauthorized, unauthenticated, session_expired, invalid_credentials' },
  { status: '403', reason: 'Forbidden', codes: 'forbidden, user_disabled, builtin_role' },
  { status: '404', reason: 'Not Found', codes: 'not_found, backup_not_found' },
  { status: '405', reason: 'Method Not Allowed', codes: 'method_not_allowed' },
  { status: '409', reason: 'Conflict', codes: 'email_taken, role_already_exists, group_already_exists, backups_disabled, backup_in_progress, backup_checksum_mismatch, backup_schema_mismatch, backup_invalid_archive' },
  { status: '413', reason: 'Payload Too Large', codes: 'payload_too_large' },
  { status: '500', reason: 'Internal Server Error', codes: 'internal_error, backup_engine_failed, backup_failed, backup_interrupted, restore_failed' },
  { status: '502', reason: 'Bad Gateway', codes: 'oauth_not_configured, oauth_state_mismatch, oauth_failed, oauth_no_email' },
  { status: '503', reason: 'Service Unavailable', codes: 'backup_engine_unavailable, restore_busy, restore_in_progress' },
]

/** Codes an adapter produces; they say something about the resource, not the caller. */
export const adapterCodes = [
  { code: 'resource_unreachable', meaningKey: 'codeUnreachable' },
  { code: 'adapter_timeout', meaningKey: 'codeTimeout' },
  { code: 'provider_malformed', meaningKey: 'codeMalformed' },
  { code: 'provider_unauthorized', meaningKey: 'codeProviderUnauthorized' },
  { code: 'cli_command_not_allowed', meaningKey: 'codeCliDenied' },
  { code: 'unknown_kind', meaningKey: 'codeUnknownKind' },
]
