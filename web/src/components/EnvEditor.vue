<script setup>
// Key/value editor for the environment block of an MCP or CLI resource.
//
// It is its own component because both kinds carry one and the pair semantics
// (rename a key, drop a row, keep the order) would otherwise be duplicated in
// two branches of the register drawer.
import { computed } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { Plus, Trash2 } from 'lucide-vue-next'

const props = defineProps({
  modelValue: { type: Object, default: () => ({}) },
})
const emit = defineEmits(['update:modelValue'])
const { t } = useCopy('registry')

const rows = computed(() => Object.entries(props.modelValue ?? {}).map(([key, value]) => ({ key, value })))

function commit(entries) {
  const next = {}
  for (const row of entries) {
    // A row without a key is still being typed, not stored.
    if (row.key) next[row.key] = row.value
  }
  emit('update:modelValue', next)
}

function update(index, field, value) {
  commit(rows.value.map((row, i) => (i === index ? { ...row, [field]: value } : row)))
}

function add() {
  commit([...rows.value, { key: '', value: '' }])
}

function remove(index) {
  commit(rows.value.filter((_, i) => i !== index))
}
</script>

<template>
  <div class="env-editor">
    <div v-for="(row, index) in rows" :key="index" class="env-row">
      <input
        class="mono"
        type="text"
        :value="row.key"
        :placeholder="t('envKey')"
        :aria-label="t('envKey')"
        @input="update(index, 'key', $event.target.value)"
      />
      <input
        class="mono"
        type="text"
        :value="row.value"
        :placeholder="t('envValue')"
        :aria-label="t('envValue')"
        @input="update(index, 'value', $event.target.value)"
      />
      <button class="btn btn-ghost" type="button" :aria-label="t('actionRemove')" @click="remove(index)">
        <Trash2 :size="14" />
      </button>
    </div>
    <button class="btn btn-ghost" type="button" @click="add">
      <Plus :size="14" />
      {{ t('actionAdd') }}
    </button>
  </div>
</template>

<style scoped>
.env-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.env-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.env-row input {
  flex: 1;
  min-width: 0;
}
</style>
