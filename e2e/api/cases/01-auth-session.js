import {
  expectAvailable, expectStatus, expectStatusAndCode, expectShape, expectArray, skip,
} from '../../lib/assertions.js'
import { uniqueName } from '../fixtures.js'

const group = 'identity & session'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('auth config describes the login screen', 'GET /api/auth/config', async (ctx) => {
    const res = await ctx.anon('GET', '/api/auth/config')
    expectAvailable(res, 'auth config')
    expectShape(res, ['providers', 'password', 'registrationOpen'], 'auth config')
    expectArray(res, 'providers', 'auth config')
  }),

  c('login accepts the configured account', 'POST /api/auth/login', async (ctx) => {
    const res = await ctx.anon('POST', '/api/auth/login', {
      body: { email: ctx.config.email, password: ctx.config.password },
    })
    expectStatus(res, 200, 'login')
    expectShape(res, ['user'], 'login')
  }),

  c('login rejects a wrong password', 'POST /api/auth/login', async (ctx) => {
    const res = await ctx.anon('POST', '/api/auth/login', {
      body: { email: ctx.config.email, password: `wrong-${uniqueName('pw')}` },
    })
    expectStatus(res, [401, 429], 'bad credentials')
    if (res.code !== 'invalid_credentials' && res.code !== 'account_locked') {
      throw new Error(`expected invalid_credentials, got ${res.code}`)
    }
  }),

  c('registering an existing email is refused', 'POST /api/auth/register', async (ctx) => {
    const res = await ctx.anon('POST', '/api/auth/register', {
      body: { email: ctx.config.email, password: 'another-password-123', name: 'e2e duplicate' },
    })
    // A closed-registration deployment answers 403; both paths prove the guard.
    expectStatus(res, [400, 403, 409], 'duplicate registration')
    if (!res.code) throw new Error('duplicate registration returned no machine code')
  }),

  c('me reports the signed-in principal', 'GET /api/auth/me', async (ctx) => {
    const res = await ctx.api('GET', '/api/auth/me')
    expectStatus(res, 200, 'me')
    expectShape(res, ['user', 'permissions'], 'me')
    if (ctx.identity.mode === 'login' && res.json.user?.email !== ctx.config.email) {
      throw new Error('me reports a different account than the one we logged in as')
    }
    if (!Array.isArray(res.json.permissions) || res.json.permissions.length === 0) {
      throw new Error('the run identity carries no permissions; use an owner or a full-scope API key')
    }
  }),

  c('my sessions are listed with a current marker', 'GET /api/auth/sessions', async (ctx) => {
    const res = await ctx.api('GET', '/api/auth/sessions')
    expectAvailable(res, 'session list')
    expectArray(res, 'sessions', 'session list')
    ctx.state.sessionIds = res.json.sessions.map((s) => s.id)
  }),

  c('revoking an unknown session is a clean 404', 'DELETE /api/auth/sessions/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/auth/sessions/${uniqueName('e2e-no-session')}`, { body: {} })
    expectStatus(res, 404, 'unknown session')
  }),

  c('logout requires a credential', 'POST /api/auth/logout', async (ctx) => {
    // A real logout would drop the one session the whole run depends on, so the
    // destructive path lives in 99-optin.js behind an explicit switch.
    const res = await ctx.anon('POST', '/api/auth/logout', { body: {} })
    expectStatus(res, 401, 'anonymous logout')
  }),

  c('oauth start redirects or reports it is unconfigured', 'GET /api/auth/oauth/{provider}', async (ctx) => {
    const res = await ctx.anon('GET', '/api/auth/oauth/google')
    if (res.status === 502 && res.code === 'oauth_not_configured') {
      skip('no OAuth provider configured on this instance')
    }
    expectStatus(res, [302, 303, 502], 'oauth start')
    if (res.status === 302 || res.status === 303) {
      if (!res.location) throw new Error('oauth start redirected without a Location header')
    }
  }),

  c('oauth callback refuses a bad state', 'GET /api/auth/oauth/{provider}/callback', async (ctx) => {
    const res = await ctx.anon('GET', '/api/auth/oauth/google/callback?code=e2e&state=invalid-e2e')
    if (res.status === 502 && res.code === 'oauth_not_configured') {
      skip('no OAuth provider configured on this instance')
    }
    expectStatus(res, [302, 303, 400, 502], 'oauth callback')
    if ((res.status === 302 || res.status === 303) && !/error=/.test(res.location || '')) {
      throw new Error('a forged state did not land on the login error path')
    }
  }),
]
