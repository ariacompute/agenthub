<script setup>
// The "you have unsaved changes" prompt, shown when an overlay is dismissed
// with edits in it.
//
// Three answers, because a two-way confirm would lose data either way: save
// and close, close without saving, or stay and keep editing. Escape and the
// backdrop both mean "stay" — the destructive answer is never the default.
import { onMounted, ref } from 'vue'
import { useCopy } from '../lib/use-copy.js'
import { useEscape } from '../lib/use-dismiss.js'

const props = defineProps({
  title: { type: String, default: '' },
  body: { type: String, default: '' },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['save', 'discard', 'cancel'])

const shell = useCopy('shell')
const saveButton = ref(null)

// Registered after the drawer it belongs to, so this is the topmost layer and
// Escape cancels the prompt instead of closing the drawer underneath.
useEscape(() => !props.busy, () => emit('cancel'))

onMounted(() => saveButton.value?.focus())
</script>

<template>
  <div class="drawer-backdrop prompt-layer" @click.self="emit('cancel')">
    <div class="card drawer prompt" role="dialog" aria-modal="true" :aria-label="title || shell.t('unsavedTitle')">
      <div class="page-head">
        <h2>{{ title || shell.t('unsavedTitle') }}</h2>
        <p class="page-sub">{{ body || shell.t('unsavedBody') }}</p>
      </div>
      <div class="drawer-actions">
        <button ref="saveButton" class="btn btn-primary" type="button" :disabled="busy" @click="emit('save')">
          {{ shell.t('actionSave') }}
        </button>
        <button class="btn btn-ghost" type="button" :disabled="busy" @click="emit('discard')">
          {{ shell.t('actionDiscard') }}
        </button>
        <button class="btn btn-ghost" type="button" :disabled="busy" @click="emit('cancel')">
          {{ shell.t('actionCancel') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Above the drawer it interrupts: the drawer backdrop is z-index 40. */
.prompt-layer {
  z-index: 60;
}

.prompt {
  width: min(420px, 100%);
}

.drawer-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-block-start: 8px;
}
</style>
