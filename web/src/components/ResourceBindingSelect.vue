<script setup>
// Multi-select of registered resources, used by the agent drawer to bind mcp
// servers and skill manuals.
//
// It edits an array of resource ids. A value that is not among the options is
// kept and shown as "unresolved" rather than dropped: the drawer must never
// silently delete a reference an operator cannot currently see (a record
// outside their grants, or an agent saved before bindings existed and holding
// free-text skill names).
import { computed, ref } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { filterOptions, toggleRef, unresolvedRefs } from '../lib/binding.js'
import { Search, X } from 'lucide-vue-next'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
  options: { type: Array, default: () => [] },
  inputId: { type: String, required: true },
})
const emit = defineEmits(['update:modelValue'])
const { t } = useCopy('registry')

const query = ref('')

const selected = computed(() => (props.modelValue ?? []).filter(Boolean))
// Anything selected but not offered: a stale id, a record outside the caller's
// grants, or a legacy free-text label.
const unresolved = computed(() => unresolvedRefs(props.modelValue, props.options))
const offered = computed(() => (props.options ?? []).filter((entry) => selected.value.includes(entry.id)))
const filtered = computed(() => filterOptions(props.options, query.value))

function toggle(id) {
  emit('update:modelValue', toggleRef(props.modelValue, id))
}
</script>

<template>
  <div class="binding-select">
    <label class="binding-search">
      <Search :size="14" aria-hidden="true" />
      <input
        :id="inputId"
        type="search"
        :value="query"
        :placeholder="t('searchPlaceholder')"
        :aria-label="t('searchPlaceholder')"
        @input="query = $event.target.value"
      />
    </label>

    <p v-if="!options.length" class="muted note">{{ t('bindingEmpty') }}</p>

    <ul v-else class="binding-options">
      <li v-for="entry in filtered" :key="entry.id" class="binding-option">
        <label class="check">
          <input type="checkbox" :checked="selected.includes(entry.id)" @change="toggle(entry.id)" />
          <span class="binding-name">{{ entry.display_name || entry.name }}</span>
          <span class="mono muted binding-id">{{ entry.id }}</span>
        </label>
      </li>
    </ul>
    <p v-if="options.length && !filtered.length" class="muted note">{{ t('empty') }}</p>

    <div v-if="selected.length" class="binding-chips">
      <span v-for="entry in offered" :key="entry.id" class="chip">
        {{ entry.display_name || entry.name }}
        <button type="button" class="chip-x" :aria-label="t('actionRemove')" @click="toggle(entry.id)">
          <X :size="12" />
        </button>
      </span>
      <span v-for="id in unresolved" :key="id" class="chip chip-unresolved" :title="t('bindingUnresolvedHint')">
        {{ id }}
        <button type="button" class="chip-x" :aria-label="t('actionRemove')" @click="toggle(id)">
          <X :size="12" />
        </button>
        <span class="chip-tag">{{ t('bindingUnresolved') }}</span>
      </span>
    </div>
  </div>
</template>

<style scoped>
.binding-select {
  display: flex;
  flex-direction: column;
  gap: 8px;
  /* A drawer card can be 300px wide; without this the id column pushes the
     whole row out of the card. */
  min-width: 0;
}

.binding-search {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 0 10px;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
  background: var(--bg-elevated);
  color: var(--text-3);
  min-width: 0;
}

.binding-search input {
  border: none;
  background: transparent;
  padding-inline: 0;
  flex: 1;
  min-width: 0;
}

.binding-search input:focus {
  box-shadow: none;
}

.binding-options {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 168px;
  overflow-y: auto;
  border: 1px solid var(--stroke-2);
  border-radius: var(--radius-sm);
}

.binding-option {
  padding: 6px 10px;
  border-block-end: 1px solid var(--stroke-2);
  min-width: 0;
}

.binding-option:last-child {
  border-block-end: none;
}

.binding-option .check {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex-wrap: wrap;
  min-height: 32px;
}

.binding-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.binding-id {
  font-size: 11px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.binding-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--stroke-2);
  border-radius: 999px;
  font-size: 12px;
  min-width: 0;
  max-width: 100%;
}

.chip-unresolved {
  border-style: dashed;
}

.chip-tag {
  font-size: 10px;
  color: var(--text-3);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.chip-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--text-3);
  cursor: pointer;
}

.chip-x:hover {
  color: var(--text-1);
  background: var(--bg-elevated);
}
</style>
