<script setup>
// Health indicator: luminance + fill + motion instead of hue. The text label is
// always rendered too, so the state is never communicated by colour alone.
import { computed } from 'vue'
import { enumLabel, useCopy } from '../lib/use-copy.js'

const props = defineProps({
  state: { type: String, default: 'unknown' },
  showLabel: { type: Boolean, default: true },
})

const { t } = useCopy('registry')
const label = computed(() => enumLabel('health', props.state))
const title = computed(() => `${t('healthLabel')}: ${label.value}`)
</script>

<template>
  <span class="state-wrap" :title="title">
    <i class="state-dot" :class="state" aria-hidden="true"></i>
    <span v-if="showLabel" class="muted">{{ label }}</span>
  </span>
</template>

<style scoped>
.state-wrap {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  min-width: 0;
}
</style>
