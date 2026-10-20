<script setup>
// Screen 8 — Playground: one A2A call against a registered resource, or a
// rehearsed conversation with several agents and models.
//
// Two modes share the screen but not the shape of a request. A single call is
// one task, optionally driven by a flow. A chat turn is one task per
// participant: the hub has no memory across tasks, so the transcript lives here
// and only travels to the participants when the operator asks for it.
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import { useHubStore } from '../lib/store.js'
import { errorMessage } from '../lib/errors.js'
import { isDirty, snapshot, useEscape } from '../lib/use-dismiss.js'
import UnsavedPrompt from '../components/UnsavedPrompt.vue'
import StateDot from '../components/StateDot.vue'
import { Play, Send, Square, Trash2 } from 'lucide-vue-next'
import { resumeTask, sendTask, streamTask } from '../lib/api.js'
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
  validateFlow,
} from '../lib/playground.js'

const store = useHubStore()
const router = useRouter()
const { t } = useCopy('playground')
const shell = useCopy('shell')

const mode = ref('single') // single | chat
const runMode = ref('stream') // sync | stream

const resourceId = ref('')
const text = ref('')
const run = ref(newRun())
const running = ref(false)
const runError = ref('')
const resumeText = ref('')
const controller = ref(null)
const copied = ref('')

// Flow editing happens in a drawer like every other editable surface here, so
// Escape and the unsaved prompt behave the way the rest of the console does.
const flowEnabled = ref(false)
const flowOpen = ref(false)
const flowRaw = ref('')
const askSave = ref(false)
const baseline = ref(null)

// Chat.
const participants = ref([])
const chat = ref(newChat([]))
const chatText = ref('')
const sending = ref(false)
const carry = ref(false)

const resources = computed(() => store.resources)
const candidates = computed(() => chatCandidates(resources.value))
const flowCheck = computed(() => validateFlow(flowRaw.value))
const canRun = computed(
  () =>
    !running.value &&
    Boolean(resourceId.value) &&
    text.value.trim().length > 0 &&
    (!flowEnabled.value || flowCheck.value.ok),
)
const canSend = computed(
  () => !sending.value && participants.value.length > 0 && chatText.value.trim().length > 0,
)
const statusText = computed(() => enumLabel('taskState', run.value.status || 'unknown'))

onMounted(async () => {
  await store.refreshResources()
  if (!resourceId.value && resources.value.length) resourceId.value = resources.value[0].id
})

/** Task states are not health states, but the dot reads the same three values. */
function healthOf(state) {
  if (state === 'completed') return 'healthy'
  if (state === 'failed') return 'unhealthy'
  return 'unknown'
}

async function copy(value) {
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    copied.value = value
    setTimeout(() => {
      copied.value = ''
    }, 1200)
  } catch (clipboardError) {
    console.error('agenthub: clipboard unavailable', clipboardError)
  }
}

function openTrace(taskId) {
  if (!taskId) return
  store.selectTask(taskId)
  router.push('/trace')
}

function clear() {
  run.value = newRun()
  runError.value = ''
  resumeText.value = ''
  chat.value = newChat(participants.value)
}

// --- single call ----------------------------------------------------------

async function runOnce() {
  const message = text.value.trim()
  if (!message || !resourceId.value || running.value) return
  running.value = true
  runError.value = ''
  run.value = newRun()
  controller.value = new AbortController()
  try {
    const options = {
      resourceId: resourceId.value,
      text: message,
      flow: flowEnabled.value ? flowCheck.value.steps : undefined,
    }
    if (runMode.value === 'stream') {
      await streamTask(
        options,
        (frame) => {
          run.value = reduceFrame(run.value, frame)
        },
        controller.value.signal,
      )
    } else {
      run.value = runFromTask(await sendTask(options))
    }
  } catch (error) {
    if (error?.name === 'AbortError') {
      run.value = { ...run.value, status: 'aborted' }
    } else {
      runError.value = error?.code || 'internal_error'
    }
  } finally {
    running.value = false
    controller.value = null
    // The run is the measurement: the task list and the health dots both moved.
    await store.refreshTasks()
    await store.refreshResources()
  }
}

function stop() {
  controller.value?.abort()
}

async function resume() {
  const answer = resumeText.value.trim()
  if (!answer || !run.value.taskId) return
  try {
    run.value = runFromTask(await resumeTask(run.value.taskId, answer))
    resumeText.value = ''
    await store.refreshTasks()
  } catch (error) {
    runError.value = error?.code || 'internal_error'
  }
}

// --- flow drawer ----------------------------------------------------------

function openFlow() {
  if (!flowRaw.value) {
    flowRaw.value = JSON.stringify(flowTemplate('single', [resourceId.value]), null, 2)
  }
  baseline.value = snapshot(flowRaw.value)
  flowOpen.value = true
}

function closeFlow() {
  flowOpen.value = false
  askSave.value = false
  baseline.value = null
}

function requestCloseFlow() {
  if (isDirty(baseline.value, flowRaw.value)) {
    askSave.value = true
    return
  }
  closeFlow()
}

// Keeping the edit and discarding it both close the drawer; only one of them
// leaves the text alone.
function keepFlow() {
  askSave.value = false
  closeFlow()
}

function discardFlow() {
  if (baseline.value !== null) flowRaw.value = JSON.parse(baseline.value)
  closeFlow()
}

useEscape(() => flowOpen.value, requestCloseFlow)

function applyTemplate(kind) {
  const ids = mode.value === 'chat' ? participants.value.map((p) => p.id) : [resourceId.value]
  flowRaw.value = JSON.stringify(flowTemplate(kind, ids.filter(Boolean)), null, 2)
}

// --- chat -----------------------------------------------------------------

function toggleParticipant(resource) {
  // Swapping the cast mid turn would leave replies without an owner.
  if (sending.value) return
  const present = participants.value.some((p) => p.id === resource.id)
  if (present) {
    participants.value = participants.value.filter((p) => p.id !== resource.id)
  } else if (canAddParticipant(participants.value.length)) {
    participants.value = [...participants.value, { id: resource.id, name: resource.name, kind: resource.kind }]
  } else {
    return
  }
  chat.value = { ...chat.value, participants: participants.value }
}

async function sendChat() {
  const message = chatText.value.trim()
  if (!message || !participants.value.length || sending.value) return
  const payload = carry.value ? `${carryContext(chat.value, CARRY_TURNS)}\n${message}` : message
  const opened = beginTurn(chat.value, message)
  chat.value = opened.chat
  chatText.value = ''
  sending.value = true
  runError.value = ''
  controller.value = new AbortController()
  try {
    await Promise.all(
      participants.value.map((participant) =>
        runParticipant(opened.turn.id, participant, payload, controller.value.signal),
      ),
    )
  } finally {
    sending.value = false
    controller.value = null
    await store.refreshTasks()
  }
}

async function runParticipant(turnId, participant, payload, signal) {
  const options = { resourceId: participant.id, text: payload }
  try {
    if (runMode.value === 'stream') {
      let state = newRun()
      await streamTask(
        options,
        (frame) => {
          state = reduceFrame(state, frame)
          chat.value = applyReply(chat.value, turnId, participant.id, {
            ...replyFromRun(state, 'working'),
            status: 'working',
          })
        },
        signal,
      )
      chat.value = applyReply(chat.value, turnId, participant.id, replyFromRun(state, state.status || 'completed'))
    } else {
      const task = await sendTask(options)
      const state = runFromTask(task)
      chat.value = applyReply(chat.value, turnId, participant.id, replyFromRun(state, state.status || 'completed'))
    }
  } catch (error) {
    if (error?.name === 'AbortError') {
      chat.value = applyReply(chat.value, turnId, participant.id, { status: 'aborted' })
    } else {
      chat.value = applyReply(chat.value, turnId, participant.id, {
        status: 'failed',
        error: error?.code || 'internal_error',
      })
    }
  }
}

async function resumeParticipant(turnId, participant, answer) {
  if (!answer) return
  try {
    const task = await resumeTask(participant.reply.taskId, answer)
    chat.value = applyReply(chat.value, turnId, participant.id, replyFromRun(runFromTask(task)))
    await store.refreshTasks()
  } catch (error) {
    runError.value = error?.code || 'internal_error'
  }
}

function replyStatus(reply) {
  if (reply.status === 'aborted') return t('actionStopped')
  return enumLabel('taskState', reply.status || 'working')
}
</script>

<template>
  <section>
    <div class="page-head">
      <h1>{{ t('title') }}</h1>
      <p class="page-sub">{{ t('subtitle') }}</p>
    </div>

    <div v-if="runError" class="error-line">
      <span>{{ errorMessage(runError) }}</span>
      <button class="btn btn-ghost" type="button" @click="runError = ''">
        {{ shell.t('actionClose') }}
      </button>
    </div>

    <div class="toolbar">
      <div class="tabs" role="tablist" :aria-label="t('title')">
        <button class="tab" :class="{ active: mode === 'single' }" type="button" @click="mode = 'single'">
          {{ t('modeSingle') }}
        </button>
        <button class="tab" :class="{ active: mode === 'chat' }" type="button" @click="mode = 'chat'">
          {{ t('modeChat') }}
        </button>
      </div>
      <div class="spacer"></div>
      <div class="tabs">
        <button class="tab" :class="{ active: runMode === 'sync' }" type="button" @click="runMode = 'sync'">
          {{ t('runSync') }}
        </button>
        <button class="tab" :class="{ active: runMode === 'stream' }" type="button" @click="runMode = 'stream'">
          {{ t('runStream') }}
        </button>
      </div>
      <button class="btn btn-ghost" type="button" @click="clear">
        <Trash2 :size="14" />
        {{ t('actionClear') }}
      </button>
    </div>

    <!-- Single call: build on the left, what came back on the right. -->
    <div v-if="mode === 'single'" class="split">
      <form class="card build" @submit.prevent="running ? stop() : runOnce()">
        <div class="field">
          <label for="pg-resource">{{ t('resourceLabel') }}</label>
          <select id="pg-resource" v-model="resourceId">
            <option v-for="resource in resources" :key="resource.id" :value="resource.id">
              {{ resource.name }} · {{ enumLabel('kind', resource.kind) }}
            </option>
          </select>
        </div>
        <div class="field">
          <label for="pg-text">{{ t('messageLabel') }}</label>
          <textarea
            id="pg-text"
            v-model="text"
            class="mono"
            rows="4"
            :placeholder="t('messagePlaceholder')"
            @keydown.ctrl.enter.prevent="runOnce()"
          ></textarea>
        </div>
        <label class="check">
          <input v-model="flowEnabled" type="checkbox" />
          <span>{{ t('flowToggle') }}</span>
        </label>
        <div v-if="flowEnabled" class="flow-row">
          <button class="btn btn-ghost" type="button" @click="openFlow">{{ t('flowJson') }}</button>
          <span class="muted note">{{ t(flowCheck.reason) }}</span>
        </div>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="submit" :disabled="!canRun && !running">
            <component :is="running ? Square : Play" :size="14" />
            {{ running ? t('actionStop') : t('actionRun') }}
          </button>
          <button class="btn btn-ghost" type="button" :disabled="!run.taskId" @click="openTrace(run.taskId)">
            {{ t('actionTrace') }}
          </button>
        </div>
      </form>

      <div class="card result" aria-live="polite">
        <div v-if="run.status || run.text || run.artifacts.length">
          <div class="result-head">
            <StateDot :state="healthOf(run.status)" :show-label="false" />
            <span class="chip">{{ run.status === 'aborted' ? t('actionStopped') : statusText }}</span>
            <span v-if="run.taskId" class="mono muted">{{ run.taskId }}</span>
            <button v-if="run.taskId" class="btn btn-ghost" type="button" @click="copy(run.taskId)">
              {{ copied === run.taskId ? '✓' : shell.t('actionCopy') }}
            </button>
          </div>
          <p v-if="run.text" class="mono result-text">{{ run.text }}</p>
          <div v-if="run.artifacts.length" class="artifacts">
            <h3>{{ t('artifactsLabel') }}</h3>
            <details v-for="(artifact, index) in run.artifacts" :key="`${artifact.name}-${index}`">
              <summary class="mono">{{ artifact.name || `artifact ${index + 1}` }}</summary>
              <pre class="mono">{{ (artifact.parts ?? []).map((part) => part.text ?? '').join('') }}</pre>
            </details>
          </div>
          <details v-if="run.frames.length">
            <summary>{{ t('framesLabel') }} · {{ run.frames.length }}</summary>
            <ol class="frames mono">
              <li v-for="(frame, index) in run.frames" :key="index">{{ frame.event }} — {{ frame.summary }}</li>
            </ol>
          </details>
          <p v-if="run.dropped" class="muted note">{{ t('framesDropped') }}</p>
          <div v-if="run.awaitingInput" class="resume">
            <h3>{{ t('awaitingInput') }}</h3>
            <textarea v-model="resumeText" class="mono" rows="2" :placeholder="t('messagePlaceholder')"></textarea>
            <button class="btn" type="button" :disabled="!resumeText.trim()" @click="resume">
              {{ t('actionResume') }}
            </button>
          </div>
        </div>
        <div v-else class="empty">{{ t('resultEmpty') }}</div>
      </div>
    </div>

    <!-- Chat: the cast on top, the conversation underneath. -->
    <div v-else class="chat">
      <div class="card cast">
        <div class="field">
          <span class="label">{{ t('participantsLabel') }}</span>
          <div class="chip-row">
            <button
              v-for="resource in candidates"
              :key="resource.id"
              class="chip chip-toggle"
              type="button"
              :class="{ active: participants.some((p) => p.id === resource.id) }"
              :disabled="sending || (!participants.some((p) => p.id === resource.id) && participants.length >= MAX_PARTICIPANTS)"
              @click="toggleParticipant(resource)"
            >
              {{ resource.name }} · {{ enumLabel('kind', resource.kind) }}
            </button>
          </div>
          <p v-if="!candidates.length" class="muted note">{{ t('participantsEmpty') }}</p>
          <p v-else class="muted note">{{ t('participantsHint') }} {{ t('participantsLimit') }}</p>
        </div>
        <label class="check">
          <input v-model="carry" type="checkbox" />
          <span>{{ t('chatCarry') }}</span>
        </label>
        <p class="muted note">{{ t('chatCarryHint') }}</p>
        <div class="composer">
          <textarea
            v-model="chatText"
            class="mono"
            rows="2"
            :placeholder="t('messagePlaceholder')"
            @keydown.enter.exact.prevent="sendChat()"
          ></textarea>
          <button class="btn btn-primary" type="button" :disabled="!canSend && !sending" @click="sending ? stop() : sendChat()">
            <component :is="sending ? Square : Send" :size="14" />
            {{ sending ? t('actionStop') : t('actionSend') }}
          </button>
        </div>
      </div>

      <div class="card transcript" aria-live="polite">
        <div v-if="chat.turns.length">
          <article v-for="turn in chat.turns" :key="turn.id" class="turn">
            <p class="turn-user mono">{{ turn.text }}</p>
            <div class="bubbles">
              <div v-for="participant in chat.participants" :key="participant.id" class="bubble">
                <div class="bubble-head">
                  <StateDot :state="healthOf(turn.replies[participant.id]?.status)" :show-label="false" />
                  <b>{{ participant.name }}</b>
                  <span class="chip">{{ enumLabel('kind', participant.kind) }}</span>
                  <span class="muted note">{{ replyStatus(turn.replies[participant.id] ?? {}) }}</span>
                  <button
                    v-if="turn.replies[participant.id]?.taskId"
                    class="btn btn-ghost"
                    type="button"
                    @click="openTrace(turn.replies[participant.id].taskId)"
                  >
                    {{ t('actionTrace') }}
                  </button>
                </div>
                <p v-if="turn.replies[participant.id]?.text" class="mono bubble-text">{{ turn.replies[participant.id].text }}</p>
                <p v-else-if="turn.replies[participant.id]?.status === 'working'" class="muted note">{{ t('chatWorking') }}</p>
                <p v-else-if="turn.replies[participant.id]?.error" class="muted note">
                  {{ errorMessage(turn.replies[participant.id].error) }}
                </p>
                <details v-if="turn.replies[participant.id]?.artifacts?.length">
                  <summary>{{ t('artifactsLabel') }}</summary>
                  <pre class="mono">{{ (turn.replies[participant.id].artifacts ?? []).map((a) => (a.parts ?? []).map((p) => p.text ?? '').join('')).join('\n') }}</pre>
                </details>
                <div v-if="turn.replies[participant.id]?.awaitingInput" class="resume">
                  <span class="muted note">{{ t('awaitingInput') }}</span>
                  <textarea
                    class="mono"
                    rows="2"
                    :placeholder="t('messagePlaceholder')"
                    @keydown.enter.exact.prevent="resumeParticipant(turn.id, { ...participant, reply: turn.replies[participant.id] }, $event.target.value); $event.target.value = ''"
                  ></textarea>
                </div>
              </div>
            </div>
          </article>
        </div>
        <div v-else class="empty">{{ t('chatEmpty') }}</div>
      </div>
    </div>

    <!-- The flow editor is a drawer so Escape and the unsaved prompt apply. -->
    <div v-if="flowOpen" class="drawer-backdrop" @click.self="requestCloseFlow">
      <div class="card drawer">
        <div class="page-head">
          <h2>{{ t('flowJson') }}</h2>
        </div>
        <div class="tabs">
          <button class="tab" type="button" @click="applyTemplate('single')">{{ t('flowSingle') }}</button>
          <button class="tab" type="button" @click="applyTemplate('branch')">{{ t('flowBranch') }}</button>
          <button class="tab" type="button" @click="applyTemplate('parallel')">{{ t('flowParallel') }}</button>
        </div>
        <div class="field">
          <label for="pg-flow">{{ t('flowJson') }}</label>
          <textarea id="pg-flow" v-model="flowRaw" class="mono" rows="12" spellcheck="false"></textarea>
          <p class="muted note">{{ t(flowCheck.reason) }}</p>
        </div>
        <div class="drawer-actions">
          <button class="btn btn-primary" type="button" @click="keepFlow">{{ shell.t('actionSave') }}</button>
          <button class="btn btn-ghost" type="button" @click="requestCloseFlow">
            {{ shell.t('actionCancel') }}
          </button>
          <span class="muted note">{{ shell.t('escHint') }}</span>
        </div>
      </div>
    </div>

    <UnsavedPrompt
      v-if="askSave"
      :title="shell.t('unsavedTitle')"
      :body="shell.t('unsavedBody')"
      @save="keepFlow"
      @discard="discardFlow"
      @cancel="askSave = false"
    />
  </section>
</template>

<style scoped>
/* .card carries no padding of its own (only the wash, the border and the
   radius), so every surface on this screen sets its own: text and controls
   must never sit against the border. */
.build,
.result,
.cast,
.transcript {
  padding: 18px;
}
.build,
.cast {
  display: flex;
  flex-direction: column;
}
.split {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.result-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-block-end: 14px;
}
.result-text {
  padding: 12px 14px;
  background: var(--bg-surface);
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
}
.artifacts,
.frames {
  margin-block-start: 16px;
}
.frames {
  max-height: 220px;
  overflow: auto;
  padding: 10px 12px 10px 28px;
  font-size: 11px;
  color: var(--text-2);
}
.frames li {
  padding-block: 2px;
}
.artifacts h3 {
  margin-block-end: 8px;
  font-size: 12px;
  color: var(--text-2);
}
.artifacts pre,
.bubble pre {
  max-height: 200px;
  overflow: auto;
  margin-block-start: 8px;
  background: var(--bg-surface);
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  padding: 12px 14px;
  font-size: 11px;
}
.resume {
  margin-block-start: 16px;
  padding-block-start: 16px;
  border-block-start: 1px solid var(--stroke-1);
}
.resume h3 {
  font-size: 13px;
}
.resume textarea {
  margin-block: 10px;
}
.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-block-end: 6px;
}
.chip-toggle {
  padding: 8px 14px;
  min-height: 36px;
  background: transparent;
  border: 1px solid var(--stroke-2);
  cursor: pointer;
}
.chip-toggle.active {
  background: var(--accent-soft);
  border-color: var(--stroke-3);
  color: var(--text-1);
}
.chip-toggle:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
  margin-block-end: 8px;
}
.composer {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  margin-block-start: 16px;
}
.composer textarea {
  flex: 1;
  min-height: 62px;
}
.chat {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.transcript {
  margin-block-start: 0;
}
.turn {
  padding-block: 18px;
  border-block-end: 1px solid var(--stroke-1);
}
.turn:first-child {
  padding-block-start: 0;
}
.turn:last-child {
  border-block-end: none;
  padding-block-end: 0;
}
/* The user turn is a block of its own so a reply never reads as its
   continuation. */
.turn-user {
  padding: 10px 14px;
  border-radius: var(--radius-sm);
  background: var(--accent-softer);
  font-size: 13px;
  white-space: pre-wrap;
  word-break: break-word;
}
.bubbles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 14px;
  margin-block-start: 14px;
}
.bubble {
  background: var(--bg-surface);
  border: 1px solid var(--stroke-1);
  border-radius: var(--radius-sm);
  padding: 14px;
}
.bubble-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-block-end: 10px;
}
.bubble-text {
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}
.bubble .resume {
  margin-block-start: 14px;
  padding-block-start: 14px;
}
.note {
  font-size: 11px;
}
@media (max-width: 1024px) {
  .split {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (prefers-reduced-motion: reduce) {
  .bubble,
  .chip-toggle {
    transition: none;
  }
}
</style>
