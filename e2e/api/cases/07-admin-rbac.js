import {
  expectAvailable, expectStatus, expectStatusAndCode, expectShape,
  expectArray, expectValue, requireState,
} from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'administration & permissions'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('users are listed', 'GET /api/users', async (ctx) => {
    const res = await ctx.api('GET', '/api/users')
    expectAvailable(res, 'user list')
    expectArray(res, 'users', 'user list')
  }),

  c('create a test user', 'POST /api/users', async (ctx) => {
    const email = `e2e-${uniqueName('user')}@example.test`
    const res = await ctx.api('POST', '/api/users', {
      body: { email, password: 'e2e-password-123', name: 'E2E User', status: 'active', roles: [], groups: [] },
    })
    expectStatus(res, 201, 'create user')
    ctx.state.userId = res.json?.user?.id
    requireState(ctx, 'userId')
    ctx.cleanup(`user ${ctx.state.userId}`, () => deleteIfExists(ctx.api, `/api/users/${ctx.state.userId}`))
  }),

  c('the test user can be read', 'GET /api/users/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/users/${requireState(ctx, 'userId')}`)
    expectStatus(res, 200, 'get user')
    expectShape(res, ['user'], 'get user')
  }),

  c('the test user can be updated', 'PUT /api/users/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/users/${requireState(ctx, 'userId')}`, {
      body: { name: 'E2E User Renamed', status: 'active' },
    })
    expectStatus(res, 200, 'update user')
  }),

  c('a malformed email is refused', 'POST /api/users', async (ctx) => {
    const res = await ctx.api('POST', '/api/users', {
      body: { email: 'not-an-email', password: 'e2e-password-123', name: 'bad' },
    })
    expectStatus(res, 400, 'invalid email')
  }),

  c('permissions are enumerable', 'GET /api/permissions', async (ctx) => {
    const res = await ctx.api('GET', '/api/permissions')
    expectAvailable(res, 'permission list')
    expectArray(res, 'permissions', 'permission list')
    ctx.state.allPermissions = res.json.permissions.map((p) => p.code).filter(Boolean)
  }),

  c('roles are listed', 'GET /api/roles', async (ctx) => {
    const res = await ctx.api('GET', '/api/roles')
    expectAvailable(res, 'role list')
    expectArray(res, 'roles', 'role list')
  }),

  c('create a role', 'POST /api/roles', async (ctx) => {
    const code = (ctx.state.allPermissions || [])[0] || 'resources:read'
    const res = await ctx.api('POST', '/api/roles', {
      body: { name: uniqueName('e2e-role'), description: 'created by e2e', permissions: [code] },
    })
    expectStatus(res, 201, 'create role')
    ctx.state.roleId = res.json?.role?.id
    requireState(ctx, 'roleId')
    ctx.cleanup(`role ${ctx.state.roleId}`, () => deleteIfExists(ctx.api, `/api/roles/${ctx.state.roleId}`))
  }),

  c('a role can be replaced', 'PUT /api/roles/{id}', async (ctx) => {
    const code = (ctx.state.allPermissions || [])[0] || 'resources:read'
    const res = await ctx.api('PUT', `/api/roles/${requireState(ctx, 'roleId')}`, {
      body: { id: ctx.state.roleId, name: 'E2E Role Renamed', permissions: [code] },
    })
    expectStatus(res, 200, 'update role')
  }),

  c('an unknown permission code is refused', 'POST /api/roles', async (ctx) => {
    const res = await ctx.api('POST', '/api/roles', {
      body: { name: uniqueName('e2e-bad-role'), permissions: ['e2e:not-a-permission'] },
    })
    expectStatus(res, 400, 'unknown permission')
  }),

  c('groups are listed', 'GET /api/groups', async (ctx) => {
    const res = await ctx.api('GET', '/api/groups')
    expectAvailable(res, 'group list')
    expectArray(res, 'groups', 'group list')
  }),

  c('create a group', 'POST /api/groups', async (ctx) => {
    const res = await ctx.api('POST', '/api/groups', {
      body: { name: uniqueName('e2e-group'), description: 'created by e2e', roles: [] },
    })
    expectStatus(res, 201, 'create group')
    ctx.state.groupId = res.json?.group?.id
    requireState(ctx, 'groupId')
    ctx.cleanup(`group ${ctx.state.groupId}`, () => deleteIfExists(ctx.api, `/api/groups/${ctx.state.groupId}`))
  }),

  c('a group can be replaced', 'PUT /api/groups/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/groups/${requireState(ctx, 'groupId')}`, {
      body: { name: 'E2E Group Renamed', roles: [] },
    })
    expectStatus(res, 200, 'update group')
  }),

  c('grants are listed', 'GET /api/grants', async (ctx) => {
    const res = await ctx.api('GET', '/api/grants')
    expectAvailable(res, 'grant list')
    expectArray(res, 'grants', 'grant list')
  }),

  c('create a resource grant', 'POST /api/grants', async (ctx) => {
    const res = await ctx.api('POST', '/api/grants', {
      body: {
        resourceId: requireState(ctx, 'resourceId'),
        principalKind: 'user',
        principalId: requireState(ctx, 'userId'),
        level: 'read',
      },
    })
    expectStatus(res, 201, 'create grant')
    ctx.state.grantId = res.json?.grant?.id
    requireState(ctx, 'grantId')
    ctx.cleanup(`grant ${ctx.state.grantId}`, () => deleteIfExists(ctx.api, `/api/grants/${ctx.state.grantId}`))
  }),

  c('an invalid grant level is refused', 'POST /api/grants', async (ctx) => {
    const res = await ctx.api('POST', '/api/grants', {
      body: {
        resourceId: requireState(ctx, 'resourceId'), principalKind: 'user',
        principalId: requireState(ctx, 'userId'), level: 'owner',
      },
    })
    expectStatus(res, 400, 'invalid grant level')
  }),

  c('API keys are listed', 'GET /api/api-keys', async (ctx) => {
    const res = await ctx.api('GET', '/api/api-keys')
    expectAvailable(res, 'api key list')
    expectArray(res, 'apiKeys', 'api key list')
  }),

  c('an API key is minted with a one-time token', 'POST /api/api-keys', async (ctx) => {
    const res = await ctx.api('POST', '/api/api-keys', {
      body: { name: uniqueName('e2e-key'), scopes: ['resources:read'], expiresInDays: 1 },
    })
    expectStatus(res, 201, 'create api key')
    ctx.state.keyId = res.json?.apiKey?.id
    requireState(ctx, 'keyId')
    if (!res.json?.token) throw new Error('the token was not returned exactly once')
    ctx.state.keyToken = res.json.token
    ctx.cleanup(`api key ${ctx.state.keyId}`, () => deleteIfExists(ctx.api, `/api/api-keys/${ctx.state.keyId}`))
  }),

  c('an API key can be read', 'GET /api/api-keys/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/api-keys/${requireState(ctx, 'keyId')}`)
    expectStatus(res, 200, 'get api key')
    expectShape(res, ['apiKey'], 'get api key')
  }),

  c('an API key can be renamed', 'PUT /api/api-keys/{id}', async (ctx) => {
    const res = await ctx.api('PUT', `/api/api-keys/${requireState(ctx, 'keyId')}`, {
      body: { name: 'E2E Key Renamed', scopes: ['resources:read'] },
    })
    expectStatus(res, 200, 'update api key')
  }),

  c('the minted key authenticates a request', 'GET /api/resources', async (ctx) => {
    // Drive the freshly minted bearer token without the shared cookie jar, so
    // this proves the key itself works rather than the session.
    const token = requireState(ctx, 'keyToken')
    const res = await ctx.client.request('GET', '/api/resources', {
      mode: 'anon', headers: { Authorization: `Bearer ${token}` },
    })
    expectAvailable(res, 'minted API key authentication')
  }),

  // Deletions run last and in reverse dependency order.
  c('revoke the minted API key', 'DELETE /api/api-keys/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/api-keys/${requireState(ctx, 'keyId')}`)
    expectStatus(res, 204, 'delete api key')
    delete ctx.state.keyId
  }),

  c('revoke the grant', 'DELETE /api/grants/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/grants/${requireState(ctx, 'grantId')}`)
    expectStatus(res, 204, 'delete grant')
    delete ctx.state.grantId
  }),

  c('delete the group', 'DELETE /api/groups/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/groups/${requireState(ctx, 'groupId')}`)
    expectStatus(res, 204, 'delete group')
    delete ctx.state.groupId
  }),

  c('delete the role', 'DELETE /api/roles/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/roles/${requireState(ctx, 'roleId')}`)
    expectStatus(res, 204, 'delete role')
    delete ctx.state.roleId
  }),

  c('revoke the test user sessions', 'DELETE /api/users/{id}/sessions', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/users/${requireState(ctx, 'userId')}/sessions`, { body: {} })
    expectStatus(res, 204, 'revoke user sessions')
  }),

  c('delete the test user', 'DELETE /api/users/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/users/${requireState(ctx, 'userId')}`)
    expectStatus(res, 204, 'delete user')
    delete ctx.state.userId
  }),

  c('an unknown user is a clean 404', 'GET /api/users/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/users/e2e-no-user')
    expectStatusAndCode(res, 404, 'not_found', 'unknown user')
  }),
]
