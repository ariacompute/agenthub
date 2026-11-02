<script setup>
// The per-kind half of the register drawer.
//
// Every kind has its own descriptor — a model router with a fallback chain, an
// A2A Agent Card, an MCP server definition, a skill manual, a CLI command or an
// arbitrary schema — which is why the drawer is not one shared field list. The
// component owns no state: it edits the kind_config object the parent holds, so
// create and update take exactly the same path.
import { computed } from 'vue'
import { useCopy, enumLabel } from '../lib/use-copy.js'
import {
  ARGS_LABELS,
  ARGS_POLICIES,
  NETWORK_LABELS,
  NETWORK_MODES,
  applyBool,
  applyChoice,
  applyList,
  applyNumber,
  applyText,
  sandboxOf,
  showsSandbox,
} from '../lib/sandbox-form.js'
import EnvEditor from './EnvEditor.vue'
import { Plus, Trash2 } from 'lucide-vue-next'

const props = defineProps({
  kind: { type: String, required: true },
  modelValue: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['update:modelValue'])
const { t } = useCopy('registry')
// The execution-sandbox block has its own namespace: the same words (provider,
// network, argument policy) are shared by the console, the docs screen and the
// error taxonomy, so they are reviewed in one file.
const { t: ts } = useCopy('sandbox')

const TRANSPORTS = ['stdio', 'streamable-http', 'sse-legacy']
const PROVIDER_MODES = ['mock', 'openai_compatible']
const AGENT_CAPABILITIES = ['streaming', 'pushNotifications', 'stateTransitionHistory']

function sub(key) {
  return props.modelValue?.[key] ?? {}
}

// patch replaces one sub-object. An empty descriptor is removed rather than
// stored as {}, so a record that never configured its kind round trips cleanly.
function patch(key, value) {
  const next = { ...(props.modelValue ?? {}) }
  if (!value || Object.keys(value).length === 0) delete next[key]
  else next[key] = value
  emit('update:modelValue', next)
}

function set(key, field, value) {
  const next = { ...sub(key) }
  if (value === '' || value === null || value === undefined) delete next[field]
  else next[field] = value
  patch(key, next)
}

function setNumber(key, field, text) {
  const parsed = Number.parseInt(text, 10)
  if (Number.isNaN(parsed)) set(key, field, '')
  else set(key, field, parsed)
}

function csvText(key, field) {
  return (sub(key)[field] ?? []).join(', ')
}

function setList(key, field, text) {
  const list = text
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
  set(key, field, list.length ? list : '')
}

// The sandbox rules live in lib/sandbox-form.js so they can be tested without a
// browser; the component only forwards the result to the parent's kind_config.
function setSandbox(next) {
  patch('sandbox', next)
}

const sandbox = computed(() => sandboxOf(props.modelValue))
const showSandbox = computed(() => showsSandbox(props.kind))

const model = computed(() => sub('model'))
const agent = computed(() => sub('agent'))
const mcp = computed(() => sub('mcp'))
const skill = computed(() => sub('skill'))
const cli = computed(() => sub('cli'))
const custom = computed(() => sub('custom'))

const mode = computed(() => model.value.provider_mode || 'mock')

function toggleCapability(name) {
  const current = agent.value.capabilities ?? []
  const next = current.includes(name) ? current.filter((c) => c !== name) : [...current, name]
  set('agent', 'capabilities', next.length ? next : '')
}

function addTarget() {
  const current = model.value.targets ?? []
  patch('model', { ...model.value, targets: [...current, { name: '', model: '', weight: 1 }] })
}

function updateTarget(index, field, value) {
  const current = [...(model.value.targets ?? [])]
  current[index] = { ...current[index], [field]: field === 'weight' ? Number(value) || 0 : value }
  patch('model', { ...model.value, targets: current })
}

function removeTarget(index) {
  const current = (model.value.targets ?? []).filter((_, i) => i !== index)
  patch('model', { ...model.value, targets: current.length ? current : undefined })
}
</script>

<template>
  <div class="kind-form">
    <!-- model: a provider, or a router with an ordered fallback chain -->
    <template v-if="kind === 'model'">
      <div class="field">
        <label>{{ t('fieldProviderMode') }}</label>
        <div class="segmented">
          <button
            v-for="m in PROVIDER_MODES"
            :key="m"
            type="button"
            class="segment"
            :class="{ active: mode === m }"
            @click="set('model', 'provider_mode', m)"
          >
            {{ enumLabel('providerMode', m) }}
          </button>
        </div>
      </div>
      <template v-if="mode === 'openai_compatible'">
        <div class="field">
          <label for="k-endpoint">{{ t('fieldEndpoint') }}</label>
          <input
            id="k-endpoint"
            class="mono"
            type="text"
            :value="model.endpoint ?? ''"
            @input="set('model', 'endpoint', $event.target.value)"
          />
        </div>
        <div class="field">
          <label for="k-model">{{ t('fieldModelId') }}</label>
          <input
            id="k-model"
            class="mono"
            type="text"
            :value="model.model_id ?? ''"
            @input="set('model', 'model_id', $event.target.value)"
          />
        </div>
        <div class="field">
          <label for="k-keyenv">{{ t('fieldApiKeyEnv') }}</label>
          <input
            id="k-keyenv"
            class="mono"
            type="text"
            placeholder="OPENAI_API_KEY"
            :value="model.api_key_env ?? ''"
            @input="set('model', 'api_key_env', $event.target.value)"
          />
        </div>
        <div class="field">
          <label for="k-timeout">{{ t('fieldTimeout') }}</label>
          <input
            id="k-timeout"
            class="mono"
            type="number"
            min="0"
            :value="model.timeout_ms ?? ''"
            @input="setNumber('model', 'timeout_ms', $event.target.value)"
          />
        </div>
        <div class="field">
          <label>{{ t('fieldTargets') }}</label>
          <div v-for="(target, index) in model.targets ?? []" :key="index" class="target-row">
            <input
              class="mono"
              type="text"
              :value="target.name ?? ''"
              :placeholder="t('fieldName')"
              @input="updateTarget(index, 'name', $event.target.value)"
            />
            <input
              class="mono"
              type="text"
              :value="target.model ?? ''"
              :placeholder="t('fieldModelId')"
              @input="updateTarget(index, 'model', $event.target.value)"
            />
            <input
              class="mono weight"
              type="number"
              min="0"
              :value="target.weight ?? 0"
              :aria-label="t('fieldWeight')"
              @input="updateTarget(index, 'weight', $event.target.value)"
            />
            <button class="btn btn-ghost" type="button" :aria-label="t('actionRemove')" @click="removeTarget(index)">
              <Trash2 :size="14" />
            </button>
          </div>
          <button class="btn btn-ghost" type="button" @click="addTarget">
            <Plus :size="14" />
            {{ t('actionAdd') }}
          </button>
        </div>
        <label class="check">
          <input
            type="checkbox"
            :checked="Boolean(model.fallback_mock)"
            @change="set('model', 'fallback_mock', $event.target.checked)"
          />
          <span>{{ t('fieldFallbackMock') }}</span>
        </label>
      </template>
    </template>

    <!-- agent: the A2A Agent Card descriptor -->
    <template v-else-if="kind === 'agent'">
      <div class="field">
        <label for="k-card">{{ t('fieldCardUrl') }}</label>
        <input
          id="k-card"
          class="mono"
          type="text"
          :value="agent.card_url ?? ''"
          @input="set('agent', 'card_url', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-proto">{{ t('fieldProtocolVersion') }}</label>
        <input
          id="k-proto"
          class="mono"
          type="text"
          :value="agent.protocol_version ?? ''"
          @input="set('agent', 'protocol_version', $event.target.value)"
        />
      </div>
      <div class="field">
        <label>{{ t('fieldCapabilities') }}</label>
        <div class="check-list">
          <label v-for="cap in AGENT_CAPABILITIES" :key="cap" class="check">
            <input type="checkbox" :checked="(agent.capabilities ?? []).includes(cap)" @change="toggleCapability(cap)" />
            <span class="mono">{{ cap }}</span>
          </label>
        </div>
      </div>
      <div class="field">
        <label for="k-skills">{{ t('fieldSkills') }}</label>
        <input
          id="k-skills"
          type="text"
          :value="csvText('agent', 'skills')"
          @input="setList('agent', 'skills', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-auth">{{ t('fieldAuth') }}</label>
        <input
          id="k-auth"
          class="mono"
          type="text"
          :value="agent.auth_scheme ?? ''"
          @input="set('agent', 'auth_scheme', $event.target.value)"
        />
      </div>
    </template>

    <!-- mcp: transport decides command + args or a URL -->
    <template v-else-if="kind === 'mcp'">
      <div class="field">
        <label for="k-transport">{{ t('fieldTransport') }}</label>
        <select
          id="k-transport"
          :value="mcp.transport ?? ''"
          @change="set('mcp', 'transport', $event.target.value)"
        >
          <option value="">—</option>
          <option v-for="tr in TRANSPORTS" :key="tr" :value="tr">{{ enumLabel('transport', tr) }}</option>
        </select>
      </div>
      <template v-if="mcp.transport === 'stdio'">
        <div class="field">
          <label for="k-command">{{ t('fieldCommand') }}</label>
          <input
            id="k-command"
            class="mono"
            type="text"
            :value="mcp.command ?? ''"
            @input="set('mcp', 'command', $event.target.value)"
          />
        </div>
        <div class="field">
          <label for="k-args">{{ t('fieldArgs') }}</label>
          <input
            id="k-args"
            class="mono"
            type="text"
            :value="csvText('mcp', 'args')"
            @input="setList('mcp', 'args', $event.target.value)"
          />
        </div>
      </template>
      <template v-else>
        <div class="field">
          <label for="k-url">{{ t('fieldEndpoint') }}</label>
          <input
            id="k-url"
            class="mono"
            type="text"
            :value="mcp.url ?? ''"
            @input="set('mcp', 'url', $event.target.value)"
          />
        </div>
      </template>
      <div class="field">
        <label for="k-tools">{{ t('fieldTools') }}</label>
        <input
          id="k-tools"
          class="mono"
          type="text"
          :value="csvText('mcp', 'tool_allowlist')"
          @input="setList('mcp', 'tool_allowlist', $event.target.value)"
        />
      </div>
      <div class="field">
        <label>{{ t('fieldEnv') }}</label>
        <EnvEditor :model-value="mcp.env ?? {}" @update:model-value="set('mcp', 'env', $event)" />
      </div>
    </template>

    <!-- skill: the versioned job manual -->
    <template v-else-if="kind === 'skill'">
      <div class="field">
        <label for="k-repo">{{ t('fieldRepository') }}</label>
        <input
          id="k-repo"
          class="mono"
          type="text"
          :value="skill.repository ?? ''"
          @input="set('skill', 'repository', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-package">{{ t('fieldPackage') }}</label>
        <input
          id="k-package"
          class="mono"
          type="text"
          :value="skill.package ?? ''"
          @input="set('skill', 'package', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-triggers">{{ t('fieldTriggers') }}</label>
        <input
          id="k-triggers"
          type="text"
          :value="csvText('skill', 'triggers')"
          @input="setList('skill', 'triggers', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-steps">{{ t('fieldSteps') }}</label>
        <input
          id="k-steps"
          type="text"
          :value="csvText('skill', 'steps')"
          @input="setList('skill', 'steps', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-skill-tools">{{ t('fieldTools') }}</label>
        <input
          id="k-skill-tools"
          class="mono"
          type="text"
          :value="csvText('skill', 'tools')"
          @input="setList('skill', 'tools', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-docs">{{ t('fieldDocs') }}</label>
        <textarea
          id="k-docs"
          class="mono"
          rows="5"
          :value="skill.docs ?? ''"
          @input="set('skill', 'docs', $event.target.value)"
        ></textarea>
      </div>
    </template>

    <!-- cli: command plus the safety rails the adapter enforces -->
    <template v-else-if="kind === 'cli'">
      <div class="field">
        <label for="k-cli-command">{{ t('fieldCommand') }}</label>
        <input
          id="k-cli-command"
          class="mono"
          type="text"
          :value="cli.command ?? ''"
          @input="set('cli', 'command', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-cli-allow">{{ t('fieldAllowlist') }}</label>
        <input
          id="k-cli-allow"
          class="mono"
          type="text"
          :value="csvText('cli', 'allowlist')"
          @input="setList('cli', 'allowlist', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-cli-timeout">{{ t('fieldTimeout') }}</label>
        <input
          id="k-cli-timeout"
          class="mono"
          type="number"
          min="0"
          :value="cli.timeout_ms ?? ''"
          @input="setNumber('cli', 'timeout_ms', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-cli-max">{{ t('fieldMaxBytes') }}</label>
        <input
          id="k-cli-max"
          class="mono"
          type="number"
          min="0"
          :value="cli.max_output_bytes ?? ''"
          @input="setNumber('cli', 'max_output_bytes', $event.target.value)"
        />
      </div>
      <div class="field">
        <label for="k-cli-dir">{{ t('fieldWorkDir') }}</label>
        <input
          id="k-cli-dir"
          class="mono"
          type="text"
          :value="cli.work_dir ?? ''"
          @input="set('cli', 'work_dir', $event.target.value)"
        />
      </div>
      <div class="field">
        <label>{{ t('fieldEnv') }}</label>
        <EnvEditor :model-value="cli.env ?? {}" @update:model-value="set('cli', 'env', $event)" />
      </div>
    </template>

    <!-- custom: anything that is not one of the five -->
    <template v-else>
      <div class="field">
        <label for="k-schema">{{ t('fieldSchema') }}</label>
        <textarea
          id="k-schema"
          class="mono"
          rows="6"
          :value="custom.schema ?? ''"
          @input="set('custom', 'schema', $event.target.value)"
        ></textarea>
      </div>
    </template>

    <!-- sandbox: the execution policy that applies to the executing kinds -->
    <template v-if="showSandbox">
      <div class="sandbox-block">
        <div class="sandbox-head">
          <strong>{{ ts('sectionSandbox') }}</strong>
          <span class="sandbox-hint">{{ ts('sectionHint') }}</span>
        </div>

        <div class="sandbox-grid">
          <div class="field">
            <label for="k-sb-provider">{{ ts('fieldProvider') }}</label>
            <input
              id="k-sb-provider"
              class="mono"
              type="text"
              placeholder="docker"
              :aria-label="ts('hintInherit')"
              :value="sandbox.provider ?? ''"
              @input="setSandbox(applyText(sandbox, 'provider', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-runtime">{{ ts('fieldRuntime') }}</label>
            <input
              id="k-sb-runtime"
              class="mono"
              type="text"
              placeholder="runsc"
              :aria-label="ts('hintInherit')"
              :value="sandbox.runtime ?? ''"
              @input="setSandbox(applyText(sandbox, 'runtime', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-image">{{ ts('fieldImage') }}</label>
            <input
              id="k-sb-image"
              class="mono"
              type="text"
              placeholder="debian:bookworm-slim"
              :aria-label="ts('hintInherit')"
              :value="sandbox.image ?? ''"
              @input="setSandbox(applyText(sandbox, 'image', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-workdir">{{ ts('fieldWorkDir') }}</label>
            <input
              id="k-sb-workdir"
              class="mono"
              type="text"
              :aria-label="ts('hintInherit')"
              :value="sandbox.work_dir ?? ''"
              @input="setSandbox(applyText(sandbox, 'work_dir', $event.target.value))"
            />
          </div>
        </div>

        <div class="sandbox-grid">
          <div class="field">
            <label for="k-sb-readonly">{{ ts('fieldReadonly') }}</label>
            <select
              id="k-sb-readonly"
              :value="sandbox.readonly_root === undefined ? '' : String(sandbox.readonly_root)"
              @change="setSandbox(applyBool(sandbox, 'readonly_root', $event.target.value))"
            >
              <option value="">{{ ts('readonlyInherit') }}</option>
              <option value="false">{{ ts('readonlyWritable') }}</option>
            </select>
          </div>
          <div class="field">
            <label for="k-sb-network">{{ ts('fieldNetwork') }}</label>
            <select
              id="k-sb-network"
              :value="sandbox.network ?? ''"
              @change="setSandbox(applyChoice(sandbox, 'network', $event.target.value, NETWORK_MODES))"
            >
              <option value="">{{ ts('hintInherit') }}</option>
              <option v-for="mode in NETWORK_MODES" :key="mode" :value="mode">
                {{ ts(NETWORK_LABELS[mode]) }}
              </option>
            </select>
          </div>
          <div class="field">
            <label for="k-sb-args-policy">{{ ts('fieldArgsPolicy') }}</label>
            <select
              id="k-sb-args-policy"
              :value="sandbox.args_policy ?? ''"
              @change="setSandbox(applyChoice(sandbox, 'args_policy', $event.target.value, ARGS_POLICIES))"
            >
              <option value="">{{ ts('hintInherit') }}</option>
              <option v-for="policy in ARGS_POLICIES" :key="policy" :value="policy">
                {{ ts(ARGS_LABELS[policy]) }}
              </option>
            </select>
          </div>
        </div>

        <div v-if="sandbox.network === 'egress_allowlist'" class="field">
          <label for="k-sb-egress">{{ ts('fieldEgress') }}</label>
          <input
            id="k-sb-egress"
            class="mono"
            type="text"
            :value="csvText('sandbox', 'egress_allowlist')"
            @input="setSandbox(applyList(sandbox, 'egress_allowlist', $event.target.value))"
          />
          <p class="sandbox-hint">{{ ts('networkAllowlistHint') }}</p>
        </div>

        <div v-if="sandbox.args_policy === 'prefix'" class="field">
          <label for="k-sb-args-allow">{{ ts('fieldArgsAllow') }}</label>
          <input
            id="k-sb-args-allow"
            class="mono"
            type="text"
            :value="csvText('sandbox', 'args_allowlist')"
            @input="setSandbox(applyList(sandbox, 'args_allowlist', $event.target.value))"
          />
        </div>

        <div class="sandbox-grid">
          <div class="field">
            <label for="k-sb-cpu">{{ ts('fieldCPU') }}</label>
            <input
              id="k-sb-cpu"
              class="mono"
              type="number"
              min="0"
              step="0.1"
              :value="sandbox.cpu ?? ''"
              @input="setSandbox(applyNumber(sandbox, 'cpu', $event.target.value, { float: true }))"
            />
          </div>
          <div class="field">
            <label for="k-sb-memory">{{ ts('fieldMemory') }}</label>
            <input
              id="k-sb-memory"
              class="mono"
              type="number"
              min="0"
              :value="sandbox.memory_mb ?? ''"
              @input="setSandbox(applyNumber(sandbox, 'memory_mb', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-pids">{{ ts('fieldPids') }}</label>
            <input
              id="k-sb-pids"
              class="mono"
              type="number"
              min="0"
              :value="sandbox.pids_limit ?? ''"
              @input="setSandbox(applyNumber(sandbox, 'pids_limit', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-timeout">{{ ts('fieldTimeout') }}</label>
            <input
              id="k-sb-timeout"
              class="mono"
              type="number"
              min="0"
              :value="sandbox.timeout_ms ?? ''"
              @input="setSandbox(applyNumber(sandbox, 'timeout_ms', $event.target.value))"
            />
          </div>
          <div class="field">
            <label for="k-sb-max-output">{{ ts('fieldMaxOutput') }}</label>
            <input
              id="k-sb-max-output"
              class="mono"
              type="number"
              min="0"
              :value="sandbox.max_output_bytes ?? ''"
              @input="setSandbox(applyNumber(sandbox, 'max_output_bytes', $event.target.value))"
            />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.kind-form {
  display: flex;
  flex-direction: column;
}
.segmented {
  display: flex;
  gap: 6px;
}
.segment {
  flex: 1;
  padding: 7px 10px;
  font-size: 12px;
  color: var(--text-2);
  background: var(--bg-elevated);
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  transition: border-color 0.16s ease, color 0.16s ease;
}
.segment.active {
  color: var(--text-1);
  border-color: var(--stroke-3);
}
.check {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-2);
}
.check-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.target-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-block-end: 8px;
}
.target-row input {
  flex: 1;
  min-width: 0;
}
.target-row .weight {
  flex: 0 0 72px;
}
/* The sandbox block is separated by a hairline rather than a colour: the console
   is monochrome by rule (tests/style.spec.js), and a border carries the same
   grouping weight. */
.sandbox-block {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-block-start: 14px;
  margin-block-start: 14px;
  border-block-start: 1px solid var(--stroke-2);
}
.sandbox-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sandbox-hint {
  font-size: 11px;
  line-height: 1.5;
  color: var(--text-2);
}
.sandbox-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
@media (max-width: 520px) {
  .sandbox-grid {
    grid-template-columns: 1fr;
  }
}
</style>
