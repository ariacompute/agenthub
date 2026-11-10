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

/** The six top-level sections, in reading order. */
export const docsSections = [
  { id: 'concepts', titleKey: 'sectionConcepts', bodyKey: 'conceptsBody' },
  { id: 'a2a', titleKey: 'sectionA2a', bodyKey: 'a2aBody' },
  { id: 'http', titleKey: 'sectionHttp', bodyKey: 'httpBody' },
  { id: 'auth', titleKey: 'sectionAuth', bodyKey: 'authBody' },
  { id: 'sandbox', titleKey: 'sectionSandbox', bodyKey: 'sandboxBody' },
  { id: 'errors', titleKey: 'sectionErrors', bodyKey: 'errorsBody' },
]

/**
 * Where the single listener lives. Ports are machine data, so they render
 * verbatim in every locale; only the prose around them lives in docs-copy.js.
 * web/tests/docs-content.spec.js pins these values to config.go and
 * docker-compose.yml, so a port change cannot leave the docs behind.
 */
export const serverBase = {
  port: '3500',
  portEnv: 'AGENTHUB_HTTP_PORT',
  dbPort: '5434',
  dbPortEnv: 'POSTGRES_PORT',
}

/** One sample block shared by the /docs screen and the generated markdown. */
export const serverBaseLines = [
  `http://localhost:${serverBase.port}          ${serverBase.portEnv} (default)`,
  `localhost:${serverBase.dbPort}                 ${serverBase.dbPortEnv} (compose development database only)`,
]

/**
 * The execution layers, strongest boundary first. The layer identifier renders
 * verbatim in every locale — it is the same token the API returns — and only the
 * description crosses into docs-copy.js.
 */
export const sandboxLayers = [
  { layer: 'container', descKey: 'sandboxLayerContainerDesc' },
  { layer: 'usermode-kernel', descKey: 'sandboxLayerUserKernelDesc' },
  { layer: 'microvm', descKey: 'sandboxLayerMicrovmDesc' },
  { layer: 'process', descKey: 'sandboxLayerProcessDesc' },
  { layer: 'platform', descKey: 'sandboxLayerPlatformDesc' },
  { layer: 'remote', descKey: 'sandboxLayerRemoteDesc' },
  { layer: 'none', descKey: 'sandboxLayerNoneDesc' },
]

/**
 * The kinds that edit `kind_config.sandbox`, in the order the register drawer
 * offers them. A model endpoint and a skill manual execute nothing, so a policy
 * block there would draw a boundary that does not exist.
 */
export const sandboxPolicyKinds = ['agent', 'mcp', 'cli']

/**
 * Network modes as `GrantsNetwork` decides them.
 *
 * `mode` is the wire value and renders verbatim. Only `bridge` and `platform`
 * hand out connectivity; `egress_allowlist` stays in the vocabulary so records
 * written against the earlier docs still parse, but it is executed as `none`
 * until an egress proxy exists. That asymmetry is the reason this table is not
 * "the four ways to get network".
 */
export const sandboxNetworkModes = [
  { mode: 'none', descKey: 'sandboxNetworkNoneDesc' },
  { mode: 'egress_allowlist', descKey: 'sandboxNetworkAllowlistDesc' },
  { mode: 'bridge', descKey: 'sandboxNetworkBridgeDesc' },
  { mode: 'platform', descKey: 'sandboxNetworkPlatformDesc' },
]

/** A resource-level policy written the way the drawer writes it: snake_case. */
export const sandboxPolicySample = `{
  "kind": "cli",
  "name": "ruff",
  "kind_config": {
    "sandbox": {
      "provider": "docker",
      "runtime": "runsc",
      "image": "python:3.12-slim",
      "work_dir": "/work",
      "readonly_root": false,
      "network": "none",
      "cpu": 1.5,
      "memory_mb": 512,
      "pids_limit": 128,
      "timeout_ms": 30000,
      "max_output_bytes": 262144,
      "env_allowlist": ["PATH", "HOME"],
      "args_policy": "prefix",
      "args_allowlist": ["--check", "--select"]
    }
  }
}`

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
    params: '{ id, taskId?, historyLength?, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/cancel',
    descKey: 'mTasksCancel',
    params: '{ id, taskId?, historyLength?, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/resume',
    descKey: 'mTasksResume',
    params: '{ id, taskId?, message, metadata? }',
    response: '{ jsonrpc: "2.0", id, result: Task }',
  },
  {
    method: 'tasks/pushNotification/set',
    descKey: 'mPushSet',
    // `taskId` is the canonical field here; `id` is accepted so the same client
    // code works across every task-scoped method. When both are present the
    // canonical one wins.
    params: '{ taskId, id?, pushNotificationConfig? }',
    response: '{ jsonrpc: "2.0", id, result: PushNotificationConfig }',
  },
  {
    method: 'tasks/pushNotification/get',
    descKey: 'mPushGet',
    // Mirror image: `id` is canonical for get, `taskId` is the alias.
    params: '{ id, taskId? }',
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

/**
 * Endpoint groups, in the order the route table registers them.
 *
 * `noteKey` is optional and carries the one thing a route table cannot show:
 * what a call does *not* touch, or a contract no column can express. Backups
 * uses it for the archive's scope; resources uses it for the single display
 * label and the strict Entry decoder.
 */
export const endpointGroups = [
  { id: 'protocol', groupKey: 'groupProtocol' },
  { id: 'auth', groupKey: 'groupAuth' },
  { id: 'resources', groupKey: 'groupResources', noteKey: 'groupResourcesNote' },
  { id: 'registries', groupKey: 'groupRegistries' },
  { id: 'sources', groupKey: 'groupSources' },
  { id: 'tasks', groupKey: 'groupTasks' },
  { id: 'export', groupKey: 'groupExport' },
  { id: 'users', groupKey: 'groupUsers' },
  { id: 'roles', groupKey: 'groupRoles' },
  { id: 'backups', groupKey: 'groupBackups', noteKey: 'groupBackupsNote' },
  { id: 'apikeys', groupKey: 'groupApiKeys' },
  { id: 'ops', groupKey: 'groupOps' },
  { id: 'collaboration', groupKey: 'groupCollaboration' },
  { id: 'files', groupKey: 'groupFiles' },
  { id: 'capabilities', groupKey: 'groupCapabilities' },
  { id: 'sandbox', groupKey: 'groupSandbox' },
  { id: 'router', groupKey: 'groupRouter' },
  { id: 'audit', groupKey: 'groupAudit' },
]

/**
 * Access class for routes with no authz code. A blank `permission` used to be
 * ambiguous: discovery, registration, OAuth, the channel webhook and healthz
 * are anonymous, while logout / me / sessions / api-keys only need a signed-in
 * session. The /docs screen and the generated markdown render these as
 * `public` / `session`; any other blank is a bug.
 *
 * Keys are "METHOD /path". web/tests/docs-content.spec.js compares them with
 * the wrappers in internal/handlers/handlers.go, so a newly public route
 * cannot silently lose its authz here.
 */
export const publicEndpoints = [
  'GET /.well-known/agent.json',
  'GET /a2a/agent-card',
  'POST /a2a',
  'GET /api/auth/config',
  'POST /api/auth/register',
  'POST /api/auth/login',
  'GET /api/auth/oauth/{provider}',
  'GET /api/auth/oauth/{provider}/callback',
  'POST /api/channels/{id}/webhook',
  'GET /healthz',
]

export const sessionOnlyEndpoints = [
  'POST /api/auth/logout',
  'GET /api/auth/me',
  'GET /api/auth/sessions',
  'DELETE /api/auth/sessions/{id}',
  'GET /api/api-keys',
  'POST /api/api-keys',
  'GET /api/api-keys/{id}',
  'PUT /api/api-keys/{id}',
  'DELETE /api/api-keys/{id}',
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
    request: '{ kind, name, display_name?, endpoint?, transport?, namespace?, kind_config? }',
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
    request: '{ email, password, name, status?, roles[], groups[] }',
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
    request: '{ name?, description?, roles[]? }',
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

  // ---- collaboration (agentbuddy) ----
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/workspace',
    permission: 'resources:read',
    descKey: 'epWorkspace',
    request: '',
    response: '{ agents: [Entry], activity: [{ id, resourceId, state, updatedAt }] }',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/threads',
    permission: 'tasks:read',
    descKey: 'epThreadsList',
    request: '',
    response: '{ threads: [Thread] }',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/threads',
    permission: 'tasks:write',
    descKey: 'epThreadCreate',
    request: '{ title?, participants?: [resourceId] }',
    response: 'Thread — 201',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/threads/{id}',
    permission: 'tasks:read',
    descKey: 'epThreadGet',
    request: '',
    response: '{ thread, messages: [Message] }',
  },
  {
    group: 'collaboration',
    method: 'DELETE',
    path: '/api/threads/{id}',
    permission: 'tasks:write',
    descKey: 'epThreadDelete',
    request: '',
    response: '204 | 404 not_found',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/threads/{id}/messages',
    permission: 'tasks:write',
    descKey: 'epThreadMessage',
    request: '{ body, mentions?: [resourceId] }',
    response: '{ message, delegated: [taskId], failed: [resourceId] } — 201',
  },
  {
    group: 'collaboration',
    method: 'DELETE',
    path: '/api/threads/{id}/messages',
    permission: 'tasks:write',
    descKey: 'epThreadClear',
    request: '',
    response: '204 | 404 not_found',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/threads/{id}/events',
    permission: 'tasks:read',
    descKey: 'epThreadEvents',
    request: '',
    response: 'text/event-stream of { thread.message, thread.agent }',
  },

  // ---- channels (external messaging surfaces) ----
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/channels',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounChannels',
    request: '',
    response: 'Channel[]',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/channels',
    permission: 'tasks:write',
    descKey: 'verbCreate',
    nounKey: 'nounChannels',
    request: '{ kind, name, enabled, config, signingKeyEnv, threadId }',
    response: 'Channel — 201',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/channels/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounChannels',
    request: '',
    response: 'Channel',
  },
  {
    group: 'collaboration',
    method: 'PUT',
    path: '/api/channels/{id}',
    permission: 'tasks:write',
    descKey: 'epChannelUpdate',
    request: '{ name?, enabled, config?, signingKeyEnv?, threadId? }',
    response: 'Channel',
  },
  {
    group: 'collaboration',
    method: 'DELETE',
    path: '/api/channels/{id}',
    permission: 'tasks:write',
    descKey: 'verbDelete',
    nounKey: 'nounChannels',
    request: '',
    response: '204 | 404 channel_not_found',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/channels/{id}/deliveries',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounDeliveries',
    request: '',
    response: 'ChannelMessage[]',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/channels/{id}/webhook',
    permission: '',
    descKey: 'epChannelWebhook',
    request: 'raw provider payload (verified by signature)',
    response: 'ChannelMessage | 200 (deduplicated)',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/channels/{id}/deliver',
    permission: 'tasks:write',
    descKey: 'epChannelDeliver',
    request: '{ "message": "string" }',
    response: 'DeliverResult { status, raw }',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/channels/{id}/deliveries/{did}/retry',
    permission: 'tasks:write',
    descKey: 'epChannelRetry',
    request: '',
    response: 'DeliverResult { status, raw } | 404 delivery_not_found | 409 delivery_not_retryable | 502 delivery_failed (the receipt stays retryable)',
  },

  // ---- connectors (policy-gated external invocation of kind=mcp) ----
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/connectors',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounConnectors',
    request: '',
    response: 'ConnectorView[] { id, name, endpoint, transport, policy, health }',
  },
  {
    group: 'collaboration',
    method: 'PUT',
    path: '/api/connectors/{id}/policy',
    permission: 'resources:write',
    descKey: 'epConnectorPolicy',
    request: '{ policy: "off" | "ask_first" | "allowed" }',
    response: 'ConnectorView',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/connectors/{id}/actions',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounActions',
    request: '',
    response: '{ id, policy, actions: [{ name, description }] }',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/connectors/{id}/actions',
    permission: 'resources:write',
    descKey: 'epConnectorAction',
    request: '{ action, args? }',
    response: 'Task | 202 (ask_first parks an input-required task)',
  },
  {
    group: 'collaboration',
    method: 'GET',
    path: '/api/connectors/approvals',
    permission: 'tasks:read',
    descKey: 'epConnectorApprovals',
    request: '',
    response: 'ConnectorApprovalView[] { taskId, resourceId, connector, action, args, requestedBy, createdAt }',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/connectors/approvals/{id}/approve',
    permission: 'tasks:write',
    descKey: 'epConnectorApprove',
    request: '{ args? }',
    response: 'Task | 404 connector_approval_not_found | 409 connector_approval_closed',
  },
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/connectors/approvals/{id}/reject',
    permission: 'tasks:write',
    descKey: 'epConnectorReject',
    request: '',
    response: 'Task | 404 connector_approval_not_found | 409 connector_approval_closed',
  },

  // ---- shared files (bytes behind a local or S3-compatible backend) ----
  {
    group: 'files',
    method: 'GET',
    path: '/api/files',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounFiles',
    request: '',
    response: '{ files: File[], maxBytes, backend }',
  },
  {
    group: 'files',
    method: 'POST',
    path: '/api/files',
    permission: 'tasks:write',
    descKey: 'epFileUpload',
    request: 'multipart/form-data; first file part is the body',
    response: 'File | 413 file_too_large | 415 file_unsupported | 503 files_disabled',
  },
  {
    group: 'files',
    method: 'GET',
    path: '/api/files/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounFiles',
    request: '',
    response: 'File { id, name, mime, size, sha256, backend, createdAt } | 404 file_not_found',
  },
  {
    group: 'files',
    method: 'GET',
    path: '/api/files/{id}/content',
    permission: 'tasks:read',
    descKey: 'epFileDownload',
    request: '',
    response: 'bytes (Content-Disposition: attachment, Range supported) | 404 file_not_found | 502 file_store_error',
  },
  {
    group: 'files',
    method: 'DELETE',
    path: '/api/files/{id}',
    permission: 'tasks:write',
    descKey: 'verbDelete',
    nounKey: 'nounFiles',
    request: '',
    response: '204 | 404 file_not_found',
  },

  // ---- speech ----
  {
    group: 'collaboration',
    method: 'POST',
    path: '/api/threads/{id}/audio',
    permission: 'tasks:write',
    descKey: 'epThreadAudio',
    request: 'multipart/form-data; first file part is the audio clip',
    response: '{ transcript, taskId? }',
  },

  // ---- agent capabilities (F10–F16) ----
  // When the matching store is not wired in, every endpoint below answers 503
  // feature_unavailable (see errorGroups) instead of 404/403.
  {
    group: 'capabilities',
    method: 'GET',
    path: '/api/computers',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounComputers',
    request: '',
    response: 'Computer[] { id, ownerId, agentId, kind(browser|shell|screen), sessionRef, status, lastAction, createdAt }',
  },
  {
    group: 'capabilities',
    method: 'POST',
    path: '/api/computers',
    permission: 'resources:write',
    descKey: 'verbCreate',
    nounKey: 'nounComputers',
    request: '{ agentId, kind?(browser|shell|screen), sessionRef?, status?, lastAction? } — unknown kind falls back to browser',
    response: 'Computer — 201',
  },
  {
    group: 'capabilities',
    method: 'GET',
    path: '/api/computers/{id}',
    permission: 'resources:read',
    descKey: 'verbGet',
    nounKey: 'nounComputers',
    request: '',
    response: 'Computer | 404 computer_not_found',
  },
  {
    group: 'capabilities',
    method: 'DELETE',
    path: '/api/computers/{id}',
    permission: 'resources:write',
    descKey: 'verbDelete',
    nounKey: 'nounComputers',
    request: '',
    response: '204 — terminates the sandbox lease first, then drops the card | 404 computer_not_found',
  },
  {
    group: 'capabilities',
    method: 'GET',
    path: '/api/schedules',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounSchedules',
    request: '',
    response: 'Schedule[] { id, goal, trigger(manual|cron|event), cronExpr, enabled, nextRun, lastRunAt, lastStatus }',
  },
  {
    group: 'capabilities',
    method: 'POST',
    path: '/api/schedules',
    permission: 'tasks:write',
    descKey: 'verbCreate',
    nounKey: 'nounSchedules',
    request: '{ goal(required), trigger?(manual|cron|event), cronExpr?, enabled? } — empty goal is 400 schedule_invalid',
    response: 'Schedule — 201',
  },
  {
    group: 'capabilities',
    method: 'GET',
    path: '/api/schedules/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounSchedules',
    request: '',
    response: 'Schedule | 404 schedule_not_found',
  },
  {
    group: 'capabilities',
    method: 'PUT',
    path: '/api/schedules/{id}',
    permission: 'tasks:write',
    descKey: 'verbReplace',
    nounKey: 'nounSchedules',
    request: '{ goal?, trigger?, cronExpr?, enabled? }',
    response: 'Schedule | 404 schedule_not_found',
  },
  {
    group: 'capabilities',
    method: 'DELETE',
    path: '/api/schedules/{id}',
    permission: 'tasks:write',
    descKey: 'verbDelete',
    nounKey: 'nounSchedules',
    request: '',
    response: '204 | 404 schedule_not_found',
  },
  {
    group: 'capabilities',
    method: 'GET',
    path: '/api/knowledge',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounKnowledge',
    request: '?agentId=',
    response: 'Knowledge[] { id, agentId, chunkText, sourceRef, createdAt }',
  },

  // ---- execution sandboxes (F17) ----
  // Leases are never created through the API: a task, a computer or a schedule
  // asks the manager for one. These routes read what exists, drive one execution,
  // and let an enrolled node fetch its own work. Every one of them answers 503
  // feature_unavailable when the control plane is not wired.
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandboxes',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounSandboxes',
    request: '',
    response: '{ leases: Lease[], default: Profile, live: boolean } — Lease { id, ownerId, resourceId, scope, provider, runtime?, nodeId?, state, ref?, profile, createdAt, renewedAt, expiresAt, execs, lastExitCode, lastCommand? }',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandboxes/{id}',
    permission: 'resources:read',
    descKey: 'verbGet',
    nounKey: 'nounSandboxes',
    request: '',
    response: '{ lease: Lease, execs: ExecRecord[] } — the last 50 executions; someone else\'s lease reads as 404 sandbox_not_found',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandboxes/stats',
    permission: 'resources:read',
    descKey: 'epSandboxStats',
    request: '',
    response: '{ activeLeases, pendingJobs, onlineNodes, recentExecs, lastSweepAt, sweeps, reclaimed, lastError? }',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandboxes/events',
    permission: 'resources:read',
    descKey: 'epSandboxEvents',
    request: '?since=<seq>',
    response: 'text/event-stream, event name sandbox_event',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandboxes/{id}/exec',
    permission: 'resources:write',
    descKey: 'epSandboxExec',
    request: '{ command(required), args?, stdin?, dir?, env?, timeoutMs? } — 1 MiB body cap; argv is exec-ed directly, never through a shell',
    response: 'ExecResult { exitCode, stdout?, stderr?, truncated, durationMs, provider, ref?, receipt? } | 400 sandbox_policy_denied | 409 sandbox_lease_lost | 504 sandbox_timeout',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandboxes/{id}/renew',
    permission: 'resources:write',
    descKey: 'epSandboxRenew',
    request: '',
    response: 'Lease | 409 sandbox_lease_lost (expired, terminated, or past the instance TTL)',
  },
  {
    group: 'sandbox',
    method: 'DELETE',
    path: '/api/sandboxes/{id}',
    permission: 'resources:write',
    descKey: 'verbDelete',
    nounKey: 'nounSandboxes',
    request: '',
    response: '204, no body | 404 sandbox_not_found',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandbox-providers',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounSandboxProviders',
    request: '',
    response: '{ providers: ProviderInfo[], groups: [{ layer, providers }], layers: Layer[], default: string }',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandbox-providers/refresh',
    permission: 'resources:write',
    descKey: 'epSandboxProviderRefresh',
    request: '',
    response: 'same shape as GET /api/sandbox-providers',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandbox-nodes',
    permission: 'resources:read',
    descKey: 'verbList',
    nounKey: 'nounSandboxNodes',
    request: '',
    response: '{ nodes: Node[], waiters, pollHint } — Node { id, ownerId, name, apiKeyId?, capabilities?, status, lastSeenAt, createdAt }',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandbox-nodes',
    permission: 'resources:write',
    descKey: 'epSandboxNodeRegister',
    request: '{ id(required, 1-64 of [A-Za-z0-9._-]), name?, capabilities? } — 64 KiB body cap',
    response: 'Node | 400 bad_request | 404 sandbox_not_found (that id belongs to someone else)',
  },
  {
    group: 'sandbox',
    method: 'DELETE',
    path: '/api/sandbox-nodes/{id}',
    permission: 'resources:write',
    descKey: 'verbDelete',
    nounKey: 'nounSandboxNodes',
    request: '',
    response: '204, no body | 404 sandbox_not_found',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandbox-nodes/{id}/heartbeat',
    permission: 'tasks:write',
    descKey: 'epSandboxNodeHeartbeat',
    request: '',
    response: '{ node: Node, pollHint } | 404 sandbox_not_found',
  },
  {
    group: 'sandbox',
    method: 'GET',
    path: '/api/sandbox-nodes/{id}/jobs',
    permission: 'tasks:write',
    descKey: 'epSandboxNodeJobs',
    request: '?wait=<seconds> — default 25, capped at 60',
    response: 'Job | 204 when the queue stays empty for the whole wait',
  },
  {
    group: 'sandbox',
    method: 'POST',
    path: '/api/sandbox-nodes/{id}/jobs/{job}',
    permission: 'tasks:write',
    descKey: 'epSandboxNodeJobResult',
    request: '{ exitCode, stdout?, stderr?, truncated?, durationMs?, receipt?, error? } — 1 MiB body cap',
    response: '{ ok: true } | 404 sandbox_not_found',
  },

  // ---- router (the scheduler) ----
  {
    group: 'router',
    method: 'POST',
    path: '/api/schedules/{id}/run',
    permission: 'tasks:write',
    descKey: 'verbRunNow',
    nounKey: 'nounSchedules',
    request: '',
    response: 'Trigger — 202, needs AGENTHUB_ROUTER_ENABLED=true',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/stats',
    permission: 'tasks:read',
    descKey: 'epRouterStats',
    request: '',
    response: 'Stats { pending, queued, runningTickets, waiting, batchSize, passes, lastPassAt, subscribers, streams, live }',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/triggers',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounTriggers',
    request: '?source=&disposition=&thread=&ticket=&limit=',
    response: '{ triggers: Trigger[] }',
  },
  {
    group: 'router',
    method: 'POST',
    path: '/api/router/triggers',
    permission: 'tasks:write',
    descKey: 'verbCreate',
    nounKey: 'nounTriggers',
    request: '{ source?(manual|cron|thread|channel), sourceId?, threadId?, kind?, body?, explicit?, meta? }',
    response: 'Trigger — 202; a redelivery answers 200 { duplicate: true }',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/triggers/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounTriggers',
    request: '',
    response: 'Trigger | 404 router_trigger_not_found',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/tickets',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounTickets',
    request: '?state=&open=true&limit=',
    response: '{ tickets: Ticket[] }',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/tickets/{id}',
    permission: 'tasks:read',
    descKey: 'verbGet',
    nounKey: 'nounTickets',
    request: '',
    response: 'TicketDetail { ticket, subscriptions, assignments, decisions, trigger } | 404 router_ticket_not_found',
  },
  {
    group: 'router',
    method: 'POST',
    path: '/api/router/tickets/{id}/cancel',
    permission: 'tasks:write',
    descKey: 'epRouterTicketCancel',
    request: '',
    response: 'TicketDetail | 404 / 409 router_ticket_closed',
  },
  {
    group: 'router',
    method: 'POST',
    path: '/api/router/tickets/{id}/handback',
    permission: 'tasks:write',
    descKey: 'epRouterTicketHandback',
    request: '{ triggerId(required), reason? } — one hand-back per trigger',
    response: '{ requeued: true } | 409 router_handback_exhausted',
  },
  {
    group: 'router',
    method: 'DELETE',
    path: '/api/router/threads/{id}/subscription',
    permission: 'tasks:write',
    descKey: 'epRouterThreadUnsubscribe',
    request: '',
    response: '204, no body | 404 router_thread_not_found',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/policies',
    permission: 'tasks:read',
    descKey: 'verbList',
    nounKey: 'nounPolicies',
    request: '',
    response: '{ policies: Policy[] } — mode(off|candidate|request_only), allow, keywords, quietStart, quietEnd, resource',
  },
  {
    group: 'router',
    method: 'PUT',
    path: '/api/router/policies/{source}',
    permission: 'tasks:write',
    descKey: 'verbReplace',
    nounKey: 'nounPolicies',
    request: '{ mode(off|candidate|request_only), allow?, keywords?, quietStart?, quietEnd?, resource? }',
    response: 'Policy | 400 router_policy_invalid',
  },
  {
    group: 'router',
    method: 'POST',
    path: '/api/router/passes',
    permission: 'tasks:write',
    descKey: 'epRouterPass',
    request: '',
    response: '{ kicked: true } — 202',
  },
  {
    group: 'router',
    method: 'GET',
    path: '/api/router/events',
    permission: 'tasks:read',
    descKey: 'epRouterEvents',
    request: '?since=<seq>&subject=',
    response: 'text/event-stream, event name router_event',
  },

  // ---- audit trail ----
  // Reading what every account did is its own permission (audit:read): an
  // operator who may read the trail is not necessarily allowed to administer
  // users. The verifier is listed before the wildcard so the literal wins.
  {
    group: 'audit',
    method: 'GET',
    path: '/api/audit',
    permission: 'audit:read',
    descKey: 'verbList',
    nounKey: 'nounAuditEvents',
    request: '?from=&to=&actor=&action=&resource=&source=&outcome=&q=&before=&limit=',
    response: '{ events: AuditEvent[], stats: { total, failed, denied, written }, next }',
  },
  {
    group: 'audit',
    method: 'GET',
    path: '/api/audit/verify',
    permission: 'audit:read',
    descKey: 'epAuditVerify',
    request: '?from=&to=',
    response: '{ ok, checked, brokenAt } — brokenAt is the first row whose hash does not link',
  },
  {
    group: 'audit',
    method: 'GET',
    path: '/api/audit/{id}',
    permission: 'audit:read',
    descKey: 'verbGet',
    nounKey: 'nounAuditEvents',
    request: '',
    response: 'AuditEvent { eventId, eventTime, requestId, eventSource, eventName, readOnly, actorKind, actorId, actorLabel, sourceIPAddress, httpMethod, path, resourceId, resourceKind, status, errorCode, latencyMs, userAgent, prevHash, hash }',
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
  { status: '400', reason: 'Bad Request', codes: 'bad_request, weak_password, invalid_email, invalid_grant, unknown_permission, password_required, backup_schedule_invalid, restore_confirm_required, schedule_invalid, router_policy_invalid, channel_payload_invalid, connector_action_denied, audio_unsupported, sandbox_unknown_provider, api_key_name_required, api_key_invalid_scope' },
  { status: '401', reason: 'Unauthorized', codes: 'unauthorized, unauthenticated, session_expired, invalid_credentials, channel_signature_missing, channel_signature_invalid, channel_timestamp_expired, api_key_expired, api_key_revoked' },
  { status: '403', reason: 'Forbidden', codes: 'forbidden, user_disabled, builtin_role, skill_distill_disabled, channel_disabled, connector_policy_off, sandbox_disabled, sandbox_policy_denied' },
  { status: '404', reason: 'Not Found', codes: 'not_found, backup_not_found, computer_not_found, schedule_not_found, router_ticket_not_found, router_trigger_not_found, router_thread_not_found, channel_not_found, delivery_not_found, connector_not_found, sandbox_not_found, file_not_found, connector_approval_not_found, api_key_not_found' },
  { status: '405', reason: 'Method Not Allowed', codes: 'method_not_allowed' },
  { status: '409', reason: 'Conflict', codes: 'email_taken, role_already_exists, group_already_exists, backups_disabled, backup_in_progress, backup_checksum_mismatch, backup_schema_mismatch, backup_invalid_archive, backup_table_guarded, router_ticket_closed, router_handback_exhausted, router_resource_unset, delivery_not_retryable, sandbox_unsupported, sandbox_lease_lost, sandbox_capability_unavailable, connector_approval_closed' },
  { status: '413', reason: 'Payload Too Large', codes: 'payload_too_large, audio_too_large, file_too_large' },
  { status: '415', reason: 'Unsupported Media Type', codes: 'audio_unsupported, file_unsupported' },
  { status: '422', reason: 'Unprocessable Entity', codes: 'audio_transcribe_failed' },
  { status: '429', reason: 'Too Many Requests', codes: 'account_locked, sandbox_limit_reached' },
  { status: '500', reason: 'Internal Server Error', codes: 'internal_error, backup_engine_failed, backup_failed, backup_interrupted, restore_failed' },
  { status: '502', reason: 'Bad Gateway', codes: 'oauth_not_configured, oauth_state_mismatch, oauth_failed, oauth_no_email, delivery_failed, file_store_error' },
  { status: '503', reason: 'Service Unavailable', codes: 'backup_engine_unavailable, restore_busy, restore_in_progress, feature_unavailable, speech_disabled, sandbox_node_offline, files_disabled' },
  { status: '504', reason: 'Gateway Timeout', codes: 'sandbox_timeout' },
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
