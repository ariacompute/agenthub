import { describe, expect, it } from 'vitest'
import {
  CARRY_TURNS,
  MAX_PARTICIPANTS,
  applyReply,
  beginTurn,
  canAddParticipant,
  carryContext,
  chatCandidates,
  flowTemplate,
  newChat,
  newRun,
  reduceFrame,
  replyFromRun,
  runFromTask,
  turnSettled,
  validateFlow,
} from '../src/lib/playground.js'

function step(op = 'then', resource = 'demo-agent') {
  return JSON.stringify([{ op, resource }])
}

describe('validateFlow', () => {
  it('accepts an empty editor: no flow means one hop to the resource', () => {
    for (const raw of ['', '   ', undefined, null]) {
      const result = validateFlow(raw)
      expect(result.ok).toBe(true)
      expect(result.reason).toBe('flowOk')
      expect(result.steps).toBeUndefined()
    }
  })

  it('accepts a list of steps and a single step object', () => {
    expect(validateFlow(step()).ok).toBe(true)
    expect(validateFlow('{"op":"then","resource":"demo-agent"}').steps).toHaveLength(1)
    expect(validateFlow(step()).steps[0].resource).toBe('demo-agent')
  })

  it('accepts the three ops with their required fields', () => {
    expect(validateFlow(step('then', 'r1')).ok).toBe(true)
    expect(validateFlow(step('branch').replace('"resource":"demo-agent"', '"when":"always","steps":[{"op":"then","resource":"r1"}]')).ok).toBe(true)
    expect(validateFlow('[{"op":"parallel","steps":[{"op":"then","resource":"r1"}]}]').ok).toBe(true)
  })

  it('treats a missing op as then', () => {
    expect(validateFlow('[{"resource":"r1"}]').ok).toBe(true)
  })

  it('rejects text that is not JSON', () => {
    expect(validateFlow('not json').reason).toBe('flowNotJson')
    expect(validateFlow('[{"op":').reason).toBe('flowNotJson')
  })

  it('rejects values that are not steps', () => {
    expect(validateFlow('42').reason).toBe('flowBadShape')
    expect(validateFlow('"then"').reason).toBe('flowBadShape')
    expect(validateFlow('["then"]').reason).toBe('flowBadShape')
    expect(validateFlow('[[]]').reason).toBe('flowBadShape')
  })

  it('rejects an unknown op', () => {
    expect(validateFlow(step('maybe')).reason).toBe('flowBadOp')
  })

  it('rejects a then step without a resource', () => {
    expect(validateFlow('[{"op":"then"}]').reason).toBe('flowNoResource')
    expect(validateFlow('[{"op":"then","resource":"  "}]').reason).toBe('flowNoResource')
  })

  it('rejects a branch condition the hub cannot evaluate', () => {
    expect(validateFlow('[{"op":"branch","when":"whatever"}]').reason).toBe('flowBadWhen')
  })

  it('accepts a branch without a condition: an empty guard is the default arm', () => {
    expect(validateFlow('[{"op":"branch","steps":[{"op":"then","resource":"r1"}]}]').ok).toBe(true)
    expect(validateFlow('[{"op":"branch","when":"always"}]').ok).toBe(true)
  })

  it('rejects malformed nesting', () => {
    expect(validateFlow('[{"op":"parallel"}]').reason).toBe('flowBadShape')
    expect(validateFlow('[{"op":"parallel","steps":[]}]').reason).toBe('flowBadShape')
    expect(validateFlow('[{"op":"branch","when":"always","steps":[{"op":"then"}]}]').reason).toBe('flowNoResource')
  })
})

describe('flowTemplate', () => {
  it('builds one template per primitive', () => {
    expect(flowTemplate('single', ['r1'])).toEqual([{ op: 'then', resource: 'r1' }])
    expect(flowTemplate('branch', ['r1'])[0].op).toBe('branch')
    expect(flowTemplate('parallel', ['r1', 'r2'])[0].steps).toHaveLength(2)
  })

  it('is valid once a resource is picked', () => {
    for (const kind of ['single', 'branch', 'parallel']) {
      expect(validateFlow(JSON.stringify(flowTemplate(kind, ['r1', 'r2']))).ok).toBe(true)
    }
  })

  it('refuses to send a flow that names no resource', () => {
    expect(validateFlow(JSON.stringify(flowTemplate('single', []))).reason).toBe('flowNoResource')
    expect(validateFlow(JSON.stringify(flowTemplate('branch', []))).reason).toBe('flowNoResource')
    expect(validateFlow(JSON.stringify(flowTemplate('parallel', []))).reason).toBe('flowBadShape')
  })
})

describe('reduceFrame', () => {
  it('takes the task id and the state from a status frame', () => {
    const next = reduceFrame(newRun(), { event: 'task_status_update', data: { kind: 'status-update', taskId: 't1', status: { state: 'working' } } })
    expect(next.taskId).toBe('t1')
    expect(next.status).toBe('working')
    expect(next.awaitingInput).toBe(false)
  })

  it('flags input-required as a conversation the operator can continue', () => {
    const next = reduceFrame(newRun(), { event: 'task_status_update', data: { taskId: 't1', status: { state: 'input-required' } } })
    expect(next.awaitingInput).toBe(true)
  })

  it('appends streamed message chunks but replaces a status message', () => {
    let state = newRun()
    state = reduceFrame(state, { event: 'message', data: { kind: 'message', parts: [{ text: 'he' }] } })
    state = reduceFrame(state, { event: 'message', data: { kind: 'message', parts: [{ text: 'llo' }] } })
    expect(state.text).toBe('hello')
    state = reduceFrame(state, { event: 'task_status_update', data: { kind: 'status-update', status: { state: 'completed', message: { parts: [{ text: 'done' }] } } } })
    expect(state.text).toBe('done')
  })

  it('collects artifacts', () => {
    let state = newRun()
    state = reduceFrame(state, { event: 'task_artifact_update', data: { kind: 'artifact-update', artifact: { name: 'a' } } })
    state = reduceFrame(state, { event: 'task_artifact_update', data: { kind: 'artifact-update', artifact: { name: 'b' } } })
    expect(state.artifacts.map((a) => a.name)).toEqual(['a', 'b'])
  })

  it('keeps the frame list bounded and counts what fell off', () => {
    let state = newRun()
    for (let i = 0; i < 5; i += 1) {
      state = reduceFrame(state, { event: 'message', data: { kind: 'message', parts: [{ text: String(i) }] } }, 3)
    }
    expect(state.frames).toHaveLength(3)
    expect(state.dropped).toBe(2)
    expect(state.frames[0].summary).toBe('2')
  })

  it('survives a frame without any payload', () => {
    expect(reduceFrame(newRun(), {}).taskId).toBe('')
    expect(reduceFrame(newRun(), undefined).status).toBe('')
  })
})

describe('runFromTask', () => {
  it('reads the terminal answer of message/send', () => {
    const run = runFromTask({ id: 't9', status: { state: 'completed', message: { parts: [{ text: 'ok' }] } }, artifacts: [{ name: 'a' }] })
    expect(run).toMatchObject({ taskId: 't9', status: 'completed', text: 'ok' })
    expect(run.artifacts).toHaveLength(1)
  })

  it('degenerates to an empty run for a missing task', () => {
    expect(runFromTask(undefined)).toEqual(newRun())
  })
})

describe('chat state machine', () => {
  const participants = [
    { id: 'r1', name: 'agent one', kind: 'agent' },
    { id: 'r2', name: 'model two', kind: 'model' },
  ]

  it('parks every participant as working when a turn opens', () => {
    const { chat, turn } = beginTurn(newChat(participants), 'hello')
    expect(chat.turns).toHaveLength(1)
    for (const p of participants) {
      expect(turn.replies[p.id].status).toBe('working')
    }
    expect(turnSettled(chat, turn.id)).toBe(false)
  })

  it('settles only when the last participant answers', () => {
    let { chat, turn } = beginTurn(newChat(participants), 'hello')
    chat = applyReply(chat, turn.id, 'r1', { status: 'completed', text: 'one' })
    expect(turnSettled(chat, turn.id)).toBe(false)
    chat = applyReply(chat, turn.id, 'r2', { status: 'failed', error: 'boom' })
    expect(turnSettled(chat, turn.id)).toBe(true)
  })

  it('leaves the other participants of a turn untouched', () => {
    let { chat, turn } = beginTurn(newChat(participants), 'hello')
    chat = applyReply(chat, turn.id, 'r1', { status: 'completed', text: 'one' })
    expect(chat.turns[0].replies.r2.status).toBe('working')
    expect(chat.turns[0].replies.r1.text).toBe('one')
  })

  it('reports an unknown turn or participant as not settled', () => {
    const { chat } = beginTurn(newChat(participants), 'hello')
    expect(turnSettled(chat, 'nope')).toBe(false)
    // An unknown turn id rewrites nothing: every reply keeps its state.
    const untouched = applyReply(chat, 'nope', 'r1', { status: 'completed' })
    expect(untouched.turns[0].replies.r1.status).toBe('working')
    const unknown = applyReply(chat, chat.turns[0].id, 'ghost', { status: 'completed' })
    expect(unknown.turns[0].replies.ghost.status).toBe('completed')
    expect(unknown.turns[0].replies.r1.status).toBe('working')
  })

  it('carries the recent turns and nothing before them', () => {
    let chat = newChat(participants)
    for (const text of ['first', 'second', 'third', 'fourth']) {
      const opened = beginTurn(chat, text)
      chat = applyReply(opened.chat, opened.turn.id, 'r1', { status: 'completed', text: `answer ${text}` })
      chat = applyReply(chat, opened.turn.id, 'r2', { status: 'completed', text: `model ${text}` })
    }
    const carried = carryContext(chat, CARRY_TURNS)
    expect(carried).toContain('user: fourth')
    expect(carried).toContain('agent one: answer fourth')
    expect(carried).not.toContain('first')
    expect(carryContext(chat, 0)).toBe('')
    expect(carryContext(newChat(participants))).toBe('')
  })

  it('carries an error as the participant answer', () => {
    const opened = beginTurn(newChat(participants), 'hello')
    const chat = applyReply(opened.chat, opened.turn.id, 'r1', { status: 'failed', error: 'boom' })
    expect(carryContext(chat, 1)).toContain('agent one: boom')
  })
})

describe('participants', () => {
  it('offers only agent and model resources', () => {
    const resources = [
      { id: 'a', kind: 'agent' },
      { id: 'm', kind: 'model' },
      { id: 's', kind: 'skill' },
      { id: 'c', kind: 'cli' },
    ]
    expect(chatCandidates(resources).map((r) => r.id)).toEqual(['a', 'm'])
    expect(chatCandidates([])).toEqual([])
  })

  it('caps the size of one conversation', () => {
    expect(canAddParticipant(0)).toBe(true)
    expect(canAddParticipant(MAX_PARTICIPANTS - 1)).toBe(true)
    expect(canAddParticipant(MAX_PARTICIPANTS)).toBe(false)
  })
})

describe('replyFromRun', () => {
  it('folds a run into what a chat bubble shows', () => {
    const reply = replyFromRun({ ...newRun(), taskId: 't1', text: 'hi', status: '' }, 'completed')
    expect(reply).toMatchObject({ taskId: 't1', text: 'hi', status: 'completed' })
  })
})
