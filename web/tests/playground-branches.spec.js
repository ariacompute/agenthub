import { describe, expect, it } from 'vitest'
import {
  carryContext,
  promptFor,
  reduceFrame,
  runFromTask,
  turnSettled,
  validateFlow,
} from '../src/lib/playground.js'

// The playground folds whatever the server streams into one run state, and the
// frames are shaped by whichever hub version produced them: a task id here, an
// id there, parts under `parts`, under `message.parts` or nested in a status.
// These cases pin every shape the fold accepts, plus the two places a name is
// optional.

describe('flow validation shapes', () => {
  it('accepts a branch without steps: the server builds the hop', () => {
    expect(validateFlow('{"op":"branch","when":"always"}').reason).toBe('flowOk')
    expect(validateFlow('{"op":"branch","when":"always","steps":null}').reason).toBe('flowOk')
  })

  it('refuses a parallel step without a step list', () => {
    expect(validateFlow('{"op":"parallel","steps":null}').reason).toBe('flowBadShape')
    expect(validateFlow('{"op":"parallel"}').reason).toBe('flowBadShape')
    expect(validateFlow('{"op":"parallel","steps":[]}').reason).toBe('flowBadShape')
  })

  it('refuses steps that are not a list, and a branch that wraps them', () => {
    expect(validateFlow('{"op":"parallel","steps":"one"}').reason).toBe('flowBadShape')
    expect(validateFlow('{"op":"branch","when":"always","steps":"one"}').reason).toBe('flowBadShape')
  })

  it('treats an empty editor as no flow', () => {
    expect(validateFlow('   ')).toEqual({ ok: true, reason: 'flowOk', steps: undefined })
  })
})

describe('runFromTask', () => {
  it('reads the text parts and skips the ones without text', () => {
    const run = runFromTask({ status: { state: 'completed', message: { parts: [{}, { text: 'a' }] } } })
    expect(run.text).toBe('a')
    expect(run.awaitingInput).toBe(false)
  })

  it('parks the run on input-required', () => {
    expect(runFromTask({ status: { state: 'input-required' } }).awaitingInput).toBe(true)
  })
})

describe('reduceFrame', () => {
  it('picks the task id out of every shape the hub sends', () => {
    expect(reduceFrame({ taskId: '', frames: [], artifacts: [], dropped: 0 }, { data: { id: 't2' } }).taskId).toBe('t2')
    expect(
      reduceFrame({ taskId: '', frames: [], artifacts: [], dropped: 0 }, { data: { task: { id: 't3' } } }).taskId,
    ).toBe('t3')
  })

  it('ignores a status that is not a string', () => {
    const next = reduceFrame({ taskId: '', status: 'working', text: '', frames: [], artifacts: [], dropped: 0 }, {
      data: { state: 42 },
    })
    expect(next.status).toBe('working')
  })

  it('accepts a bare string status when no status object was sent', () => {
    const next = reduceFrame({ taskId: '', status: '', text: '', frames: [], artifacts: [], dropped: 0 }, {
      data: { state: 'input-required' },
    })
    expect(next.status).toBe('input-required')
    expect(next.awaitingInput).toBe(true)
  })

  it('appends a streamed message but replaces a status message', () => {
    const state = { taskId: '', status: '', text: 'one ', frames: [], artifacts: [], dropped: 0 }
    expect(reduceFrame(state, { event: 'message', data: { parts: [{ text: 'two' }] } }).text).toBe('one two')
    expect(reduceFrame(state, { data: { status: { message: { parts: [{ text: 'two' }] } } } }).text).toBe('two')
    expect(reduceFrame(state, { data: { message: { parts: [{ text: 'three' }] } } }).text).toBe('three')
  })

  it('summarises each frame by state, text, artifact name or kind', () => {
    const base = { taskId: '', status: '', text: '', frames: [], artifacts: [], dropped: 0 }
    expect(reduceFrame(base, { data: { status: { state: 'working' } } }).frames[0].summary).toBe('working')
    expect(reduceFrame(base, { data: { parts: [{ text: 'a short line' }] } }).frames[0].summary).toBe('a short line')
    const long = reduceFrame(base, { data: { parts: [{ text: 'x'.repeat(200) }] } }).frames[0].summary
    expect(long.endsWith('…')).toBe(true)
    expect(long.length).toBe(121)
    expect(reduceFrame(base, { data: { artifact: { name: 'report.csv' } } }).frames[0].summary).toBe('report.csv')
    expect(reduceFrame(base, { data: { kind: 'status' } }).frames[0].summary).toBe('status')
    expect(reduceFrame(base, {}).frames[0].event).toBe('frame')
  })

  it('bounds the frame list and counts what it dropped', () => {
    const state = { taskId: '', status: '', text: '', frames: [], artifacts: [], dropped: 0 }
    const next = reduceFrame(state, { data: {} }, 1)
    const grown = reduceFrame(next, { data: {} }, 1)
    expect(grown.frames).toHaveLength(1)
    expect(grown.dropped).toBe(1)
  })
})

describe('chat transcripts', () => {
  const turn = {
    id: 'turn-1',
    text: 'summarise this',
    replies: { p1: { status: 'done', text: 'first' } },
  }
  const chat = {
    participants: [{ id: 'p1' }, { id: 'p2', name: 'Bee' }],
    turns: [turn],
    seq: 1,
  }

  it('reports a turn with no replies as unsettled', () => {
    expect(turnSettled({ turns: [{ id: 'turn-9', replies: {} }] }, 'turn-9')).toBe(false)
    expect(turnSettled(chat, 'missing')).toBe(false)
    expect(turnSettled({ turns: [{ id: 't', replies: { p1: { status: 'done' } } }] }, 't')).toBe(true)
  })

  it('falls back to the participant id when it has no name', () => {
    const prompt = promptFor(chat, 'turn-1', 'relay', 2)
    expect(prompt).toContain('user: summarise this')
    expect(prompt).toContain('p1: first')
  })

  it('reads the previous round in group mode', () => {
    const two = {
      participants: [{ id: 'p1', name: 'Ada' }],
      turns: [
        { id: 'turn-1', text: 'hi', replies: { p1: { status: 'done', text: 'earlier' } } },
        { id: 'turn-2', text: 'again', replies: { p1: { status: 'working', text: '' } } },
      ],
      seq: 2,
    }
    expect(promptFor(two, 'turn-2', 'group', 0)).toContain('Ada: earlier')
  })

  it('carries only the replies that exist, named or not', () => {
    const carried = carryContext(chat)
    expect(carried).toContain('user: summarise this')
    expect(carried).toContain('p1: first')
    expect(carried).not.toContain('p2')
    expect(carryContext(chat, 0)).toBe('')
    expect(carryContext({ participants: [], turns: [], seq: 0 })).toBe('')
  })
})
