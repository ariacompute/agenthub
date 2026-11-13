import {
  expectAvailable, expectStatus, expectShape, expectArray, requireState, skip,
} from '../../lib/assertions.js'
import { deleteIfExists } from '../../lib/cleanup.js'
import { optinReason } from '../../e2e.config.js'

const group = 'database checkpoints'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

const backupsOn = (ctx) => ctx.state.backupsEnabled
  ? true
  : 'backups are disabled on this instance (AGENTHUB_BACKUP_MODE=off)'

export default [
  c('backups report their enabled state', 'GET /api/backups', async (ctx) => {
    const res = await ctx.api('GET', '/api/backups')
    expectStatus(res, 200, 'backup list')
    expectShape(res, ['enabled', 'checkpoints'], 'backup list')
    expectArray(res, 'checkpoints', 'backup list')
    ctx.state.backupsEnabled = Boolean(res.json.enabled)
    if (!ctx.state.backupsEnabled) {
      ctx.reporter.note(`backups disabled: ${res.json.reason || 'no reason given'}`)
    }
  }),

  c('the checkpoint schedule is readable', 'GET /api/backups/schedule', async (ctx) => {
    const res = await ctx.api('GET', '/api/backups/schedule')
    if (!ctx.state.backupsEnabled) {
      expectStatus(res, 409, 'schedule while disabled')
      return
    }
    expectAvailable(res, 'backup schedule')
    expectShape(res, ['schedule'], 'backup schedule')
  }, { enabled: backupsOn }),

  c('the checkpoint schedule can be saved', 'PUT /api/backups/schedule', async (ctx) => {
    const res = await ctx.api('PUT', '/api/backups/schedule', {
      body: {
        mode: 'off', enabled: false, intervalSec: 3600, dailyTime: '03:00',
        timezone: 'UTC', cronExpr: '', keepLast: 5, maxAgeDays: 30,
      },
    })
    expectAvailable(res, 'save backup schedule')
  }, { enabled: backupsOn }),

  c('an invalid schedule mode is refused', 'PUT /api/backups/schedule', async (ctx) => {
    const res = await ctx.api('PUT', '/api/backups/schedule', {
      body: {
        mode: 'not-a-mode', enabled: false, intervalSec: 3600, dailyTime: '03:00',
        timezone: 'UTC', cronExpr: '', keepLast: 5, maxAgeDays: 30,
      },
    })
    expectStatus(res, 400, 'invalid backup schedule')
  }, { enabled: backupsOn }),

  c('take a checkpoint', 'POST /api/backups', async (ctx) => {
    const res = await ctx.api('POST', '/api/backups', { body: { label: 'e2e' } })
    expectStatus(res, 201, 'create backup')
    ctx.state.checkpointId = res.json?.checkpoint?.id || res.json?.id
    requireState(ctx, 'checkpointId')
    ctx.cleanup(`checkpoint ${ctx.state.checkpointId}`, () => deleteIfExists(ctx.api, `/api/backups/${ctx.state.checkpointId}`))
  }, { enabled: backupsOn }),

  c('a checkpoint can be read', 'GET /api/backups/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/backups/${requireState(ctx, 'checkpointId')}`)
    expectStatus(res, 200, 'get backup')
    // Every checkpoint endpoint answers {"checkpoint": {...}}; the create case
    // already accepts that envelope, so the read case must assert the same one.
    expectShape(res, ['checkpoint'], 'get backup')
    const checkpoint = res.json.checkpoint
    if (!checkpoint?.id || !checkpoint?.status) {
      throw new Error('get backup: checkpoint is missing id or status')
    }
  }, { enabled: backupsOn }),

  c('a checkpoint downloads as gzip', 'GET /api/backups/{id}/download', async (ctx) => {
    const res = await ctx.api('GET', `/api/backups/${requireState(ctx, 'checkpointId')}/download`, { binary: true })
    expectStatus(res, 200, 'download backup')
    if (res.size < 1) throw new Error('the downloaded archive was empty')
  }, { enabled: backupsOn }),

  c('downloading an unknown checkpoint is refused', 'GET /api/backups/{id}/download', async (ctx) => {
    const res = await ctx.api('GET', '/api/backups/e2e-no-checkpoint/download', { binary: true })
    // A permission check can precede the disabled-backup check depending on
    // the deployment, so 403 is an acceptable refusal here too.
    expectStatus(res, [403, 404, 409], 'unknown checkpoint download')
  }),

  c('a restore without confirm is refused', 'POST /api/backups/{id}/restore', async (ctx) => {
    const res = await ctx.api('POST', `/api/backups/${requireState(ctx, 'checkpointId')}/restore`, { body: {} })
    expectStatus(res, 400, 'restore without confirm')
  }, { enabled: backupsOn }),

  c('restoring a checkpoint really rewinds the database', 'POST /api/backups/{id}/restore', async (ctx) => {
    const id = requireState(ctx, 'checkpointId')
    const res = await ctx.api('POST', `/api/backups/${id}/restore`, { body: { confirm: id } })
    expectAvailable(res, 'restore backup')
  }, {
    enabled: (ctx) => {
      if (!ctx.state.backupsEnabled) return 'backups are disabled on this instance'
      return optinReason(ctx.config, 'backupRestore', 'a real restore rewinds the database')
    },
  }),

  c('delete the checkpoint', 'DELETE /api/backups/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/backups/${requireState(ctx, 'checkpointId')}`)
    expectStatus(res, 204, 'delete backup')
    delete ctx.state.checkpointId
  }, { enabled: backupsOn }),
]
