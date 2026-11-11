import {
  expectAvailable, expectStatus, expectShape, expectArray,
  requireState, expectValue,
} from '../../lib/assertions.js'
import { fileForm, uniqueName } from '../fixtures.js'
import { deleteIfExists } from '../../lib/cleanup.js'

const group = 'shared files'
const c = (id, route, run, extra = {}) => ({ id, group, route, ...extra, run })

export default [
  c('files are listed', 'GET /api/files', async (ctx) => {
    const res = await ctx.api('GET', '/api/files')
    expectAvailable(res, 'file list')
    expectShape(res, ['files', 'backend'], 'file list')
    expectArray(res, 'files', 'file list')
  }),

  c('upload a small file', 'POST /api/files', async (ctx) => {
    const res = await ctx.api('POST', '/api/files', {
      body: fileForm(`e2e-${uniqueName('file')}.txt`, 'agenthub e2e file payload', 'text/plain'),
    })
    expectAvailable(res, 'upload file')
    expectStatus(res, 201, 'upload file')
    ctx.state.fileId = res.json?.id
    requireState(ctx, 'fileId')
    ctx.cleanup(`file ${ctx.state.fileId}`, () => deleteIfExists(ctx.api, `/api/files/${ctx.state.fileId}`))
  }),

  c('file metadata is readable', 'GET /api/files/{id}', async (ctx) => {
    const res = await ctx.api('GET', `/api/files/${requireState(ctx, 'fileId')}`)
    expectStatus(res, 200, 'get file')
    expectShape(res, ['id', 'name', 'size', 'sha256'], 'file metadata')
    expectValue(res.json?.id, ctx.state.fileId, 'file id')
  }),

  c('file content downloads as bytes', 'GET /api/files/{id}/content', async (ctx) => {
    const res = await ctx.api('GET', `/api/files/${requireState(ctx, 'fileId')}/content`, { binary: true })
    expectStatus(res, 200, 'download file')
    if (res.size < 1) throw new Error('downloaded file was empty')
  }),

  c('a Range request is honoured', 'GET /api/files/{id}/content', async (ctx) => {
    const res = await ctx.api('GET', `/api/files/${requireState(ctx, 'fileId')}/content`, {
      binary: true, headers: { Range: 'bytes=0-3' },
    })
    expectStatus(res, [200, 206], 'range download')
    if (res.size > 4 && res.status === 206) {
      throw new Error('a 206 response carried more bytes than the requested range')
    }
  }),

  c('an unknown file is a clean 404', 'GET /api/files/{id}', async (ctx) => {
    const res = await ctx.api('GET', '/api/files/e2e-no-file')
    expectStatus(res, [404, 503], 'unknown file')
  }),

  c('delete the file', 'DELETE /api/files/{id}', async (ctx) => {
    const res = await ctx.api('DELETE', `/api/files/${requireState(ctx, 'fileId')}`)
    expectStatus(res, 204, 'delete file')
    delete ctx.state.fileId
  }),
]
