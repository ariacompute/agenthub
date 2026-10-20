// Pure logic behind the Playground screen: flow templates and validation, the
// bounded accumulation of streaming frames, and the chat state machine.
//
// Everything here is DOM free so it can be covered by unit tests; the view only
// renders what these functions return. The flow shapes mirror
// internal/hub.Step: the server stays the authority, this module only spares
// the operator a round trip for a typo.

export const FLOW_OPS = ['then', 'branch', 'parallel']
export const CHAT_KINDS = ['agent', 'model']
export const MAX_PARTICIPANTS = 4
export const FRAME_LIMIT = 200
export const CARRY_TURNS = 3

// How the participants of one conversation talk to each other:
//   broadcast — everybody gets the same message and answers on its own
//   relay     — they answer in order, each reading what this round produced
//   group     — everybody answers each round, reading the previous round
export const CHAT_MODES = ['broadcast', 'relay', 'group']
// How a multi round conversation knows when to stop.
export const ROUND_POLICIES = ['limit', 'manual', 'confirm']
// Hard ceiling for the "keep going until I stop" policy: an unattended
// conversation must not run forever.
export const MAX_ROUNDS = 10
export const DEFAULT_ROUNDS = { relay: 1, group: 3 }

// --- flows ----------------------------------------------------------------

/** The single hop the server builds on its own when no flow is sent. */
export function flowSingle(resourceId) {
  return [{ op: 'then', resource: resourceId }]
}

export function flowBranch(resourceId, when = 'contains:urgent') {
  return [{ op: 'branch', when, steps: [{ op: 'then', resource: resourceId }] }]
}

export function flowParallel(resourceIds = []) {
  return [{ op: 'parallel', steps: resourceIds.map((id) => ({ op: 'then', resource: id })) }]
}

export function flowTemplate(kind, resourceIds = []) {
  if (kind === 'branch') return flowBranch(resourceIds[0] ?? '')
  if (kind === 'parallel') return flowParallel(resourceIds)
  return flowSingle(resourceIds[0] ?? '')
}

function isPlainObject(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function validWhen(when) {
  const trimmed = String(when ?? '').trim()
  if (trimmed === '' || trimmed === 'always' || trimmed === 'true') return true
  return ['contains:', 'kind:', 'has:'].some((prefix) => trimmed.startsWith(prefix))
}

function checkStep(raw) {
  if (!isPlainObject(raw)) return 'flowBadShape'
  const op = String(raw.op ?? '').trim().toLowerCase() || 'then'
  if (!FLOW_OPS.includes(op)) return 'flowBadOp'
  if (op === 'then') {
    return String(raw.resource ?? '').trim() ? 'flowOk' : 'flowNoResource'
  }
  if (op === 'branch') {
    if (!validWhen(raw.when)) return 'flowBadWhen'
    return checkSteps(raw.steps)
  }
  if (!Array.isArray(raw.steps) || raw.steps.length === 0) return 'flowBadShape'
  return checkSteps(raw.steps)
}

function checkSteps(steps) {
  if (steps === undefined || steps === null) return 'flowOk'
  if (!Array.isArray(steps)) return 'flowBadShape'
  for (const step of steps) {
    const reason = checkStep(step)
    if (reason !== 'flowOk') return reason
  }
  return 'flowOk'
}

/**
 * Validates what the operator typed into the flow editor.
 *
 * An empty editor means "no flow", which the server answers with a single hop
 * to the selected resource, so it is valid. The parsed value is handed back in
 * `steps` — an array, never a raw string, because the server only accepts a
 * JSON array when the value arrives as text.
 */
export function validateFlow(raw) {
  const text = String(raw ?? '').trim()
  if (!text) return { ok: true, reason: 'flowOk', steps: undefined }
  let parsed
  try {
    parsed = JSON.parse(text)
  } catch (parseError) {
    return { ok: false, reason: 'flowNotJson', steps: undefined }
  }
  const steps = Array.isArray(parsed) ? parsed : [parsed]
  const reason = checkSteps(steps)
  return { ok: reason === 'flowOk', reason, steps }
}

// --- streaming ------------------------------------------------------------

function textOf(parts) {
  if (!Array.isArray(parts)) return ''
  return parts.map((part) => (typeof part?.text === 'string' ? part.text : '')).join('')
}

export function newRun() {
  return { taskId: '', status: '', text: '', artifacts: [], frames: [], dropped: 0, awaitingInput: false }
}

/** Folds one finished task (the answer of message/send) into a run state. */
export function runFromTask(task) {
  const status = task?.status?.state ?? ''
  return {
    taskId: task?.id ?? '',
    status,
    text: textOf(task?.status?.message?.parts),
    artifacts: task?.artifacts ?? [],
    frames: [],
    dropped: 0,
    awaitingInput: status === 'input-required',
  }
}

/**
 * Folds one streamed frame into a run state. Only the frames are bounded: a
 * long task may stream forever, so the oldest are dropped and counted instead
 * of growing the list without limit.
 */
export function reduceFrame(state, frame, limit = FRAME_LIMIT) {
  const data = frame?.data ?? {}
  const next = { ...state, artifacts: state.artifacts, frames: state.frames }
  const id = data.taskId ?? data.id ?? data.task?.id ?? ''
  if (id) next.taskId = id

  const event = frame?.event ?? ''
  const kind = data.kind ?? ''
  const state_ = data.status?.state ?? (typeof data.state === 'string' ? data.state : '')
  if (state_) {
    next.status = state_
    next.awaitingInput = state_ === 'input-required'
  }

  const incoming = textOf(data.status?.message?.parts ?? data.parts ?? data.message?.parts)
  if (incoming) {
    // A streamed message arrives in chunks and appends; a status update carries
    // the whole message and replaces.
    next.text = kind === 'message' || event === 'message' ? state.text + incoming : incoming
  }
  if (data.artifact) next.artifacts = [...state.artifacts, data.artifact]

  const frames = [...state.frames, { at: Date.now(), event: event || kind || 'frame', summary: summarize(data) }]
  if (frames.length > limit) {
    next.dropped = state.dropped + (frames.length - limit)
    next.frames = frames.slice(-limit)
  } else {
    next.frames = frames
  }
  return next
}

function summarize(data) {
  const state_ = data?.status?.state ?? ''
  if (state_) return state_
  const text = textOf(data?.parts ?? data?.message?.parts ?? data?.status?.message?.parts)
  if (text) return text.length > 120 ? `${text.slice(0, 120)}…` : text
  if (data?.artifact?.name) return data.artifact.name
  return data?.kind ?? ''
}

/** The slice of a run a chat bubble needs. */
export function replyFromRun(run, fallback = '') {
  return {
    taskId: run.taskId,
    status: run.status || fallback,
    text: run.text,
    artifacts: run.artifacts,
    awaitingInput: run.awaitingInput,
  }
}

// --- chat ----------------------------------------------------------------

/** Resources that can take part in a conversation. */
export function chatCandidates(resources = []) {
  return resources.filter((resource) => CHAT_KINDS.includes(resource.kind))
}

export function canAddParticipant(count) {
  return count < MAX_PARTICIPANTS
}

export function newChat(participants = []) {
  return { participants: participants.slice(), turns: [], seq: 0 }
}

/** Opens a turn and parks every participant as working. */
export function beginTurn(chat, text) {
  const seq = chat.seq + 1
  const replies = {}
  for (const participant of chat.participants) {
    replies[participant.id] = { status: 'working', text: '', artifacts: [], taskId: '', error: '' }
  }
  const turn = { id: `turn-${seq}`, text, replies }
  return { chat: { ...chat, seq, turns: [...chat.turns, turn] }, turn }
}

/** Records one participant's answer; the others are left untouched. */
export function applyReply(chat, turnId, resourceId, reply) {
  const turns = chat.turns.map((turn) => {
    if (turn.id !== turnId) return turn
    const current = turn.replies[resourceId] ?? {}
    return { ...turn, replies: { ...turn.replies, [resourceId]: { ...current, ...reply } } }
  })
  return { ...chat, turns }
}

/** True once no participant of the turn is still working. */
export function turnSettled(chat, turnId) {
  const turn = chat.turns.find((entry) => entry.id === turnId)
  if (!turn) return false
  const ids = Object.keys(turn.replies)
  if (ids.length === 0) return false
  return ids.every((id) => turn.replies[id]?.status !== 'working')
}

/**
 * What one participant should read before answering: the message of this round
 * plus whatever the chosen style lets it see.
 *
 * Nothing here is a server side memory — the hub only resumes a task parked in
 * input-required, so the page is what hands a participant the words of the
 * others by putting them into the prompt it sends.
 */
export function promptFor(chat, turnId, mode, index = 0) {
  const turn = chat.turns.find((entry) => entry.id === turnId)
  if (!turn) return ''
  const lines = []
  if (turn.text) lines.push(`user: ${turn.text}`)

  const speak = (participant, reply) => {
    const body = reply?.error || reply?.text
    if (body) lines.push(`${participant.name || participant.id}: ${body}`)
  }

  if (mode === 'relay') {
    // Everything said earlier in this very round, in speaking order.
    for (let i = 0; i < Math.min(index, chat.participants.length); i += 1) {
      speak(chat.participants[i], turn.replies[chat.participants[i].id])
    }
  } else if (mode === 'group') {
    // Everything the round before this one produced, whoever said it.
    const at = chat.turns.findIndex((entry) => entry.id === turnId)
    if (at > 0) {
      for (const participant of chat.participants) {
        speak(participant, chat.turns[at - 1].replies[participant.id])
      }
    }
  }
  return lines.join('\n')
}

/**
 * Whether another round starts on its own. A broadcast is one round by
 * definition, and "ask me each round" never continues without the operator.
 */
export function autoContinue(mode, policy, done, limit) {
  if (mode === 'broadcast' || policy === 'confirm') return false
  if (done >= MAX_ROUNDS) return false
  if (policy === 'limit') return done < Math.max(1, Math.min(limit, MAX_ROUNDS))
  if (policy === 'manual') return true
  return false
}

/**
 * The last few turns as a flat transcript, to be sent along with the next
 * message when the operator asks for it. Empty when there is nothing to carry.
 */
export function carryContext(chat, turns = CARRY_TURNS) {
  if (turns <= 0 || chat.turns.length === 0) return ''
  const lines = []
  for (const turn of chat.turns.slice(-turns)) {
    lines.push(`user: ${turn.text}`)
    for (const participant of chat.participants) {
      const reply = turn.replies[participant.id]
      if (!reply) continue
      const body = reply.error || reply.text
      if (body) lines.push(`${participant.name || participant.id}: ${body}`)
    }
  }
  return lines.join('\n')
}
