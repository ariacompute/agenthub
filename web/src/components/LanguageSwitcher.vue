<script setup>
// 13 languages, the same catalogue as the aria compute site.
import { computed, ref } from 'vue'
import { LANGUAGES } from '../lib/languages.js'
import { setLang, useCopy, useLang } from '../lib/use-copy.js'
import { Globe } from 'lucide-vue-next'

const lang = useLang()
const { t } = useCopy('shell')
const open = ref(false)

const current = computed(() => LANGUAGES.find(([code]) => code === lang.value)?.[1] ?? 'English')

function choose(code) {
  setLang(code)
  open.value = false
}
</script>

<template>
  <div class="lang">
    <button class="chip lang-btn" type="button" :aria-label="t('langLabel')" @click="open = !open">
      <Globe :size="14" />
      <strong>{{ current }}</strong>
    </button>
    <div v-if="open" class="lang-menu card" role="listbox">
      <button
        v-for="[code, name] in LANGUAGES"
        :key="code"
        class="lang-item"
        :class="{ active: code === lang }"
        type="button"
        role="option"
        :aria-selected="code === lang"
        @click="choose(code)"
      >
        {{ name }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.lang {
  position: relative;
}

.lang-btn {
  gap: 7px;
}

.lang-menu {
  position: absolute;
  inset-block-start: calc(100% + 8px);
  inset-inline-end: 0;
  min-width: 168px;
  max-height: 320px;
  overflow: auto;
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  z-index: 50;
}

.lang-item {
  padding: 8px 10px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--text-2);
  text-align: start;
  transition: background 0.16s ease, color 0.16s ease;
}

.lang-item:hover {
  background: var(--accent-softer);
  color: var(--text-1);
}

.lang-item.active {
  background: var(--accent-soft);
  color: var(--text-1);
  font-weight: 600;
}
</style>
