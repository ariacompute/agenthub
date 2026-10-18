<script setup>
// The mark is a hub: one centre with four peers wired to it, which is what the
// platform does with model, agent, mcp, skill and cli resources. It inherits
// currentColor, so the badge never carries a colour of its own — dark and light
// themes both get their own contrast from the same glyph.
defineProps({
  size: { type: [Number, String], default: 18 },
})
</script>

<template>
  <svg
    class="brand-glyph"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="M12 5.6v2.6M12 15.8v2.6M5.6 12h2.6M15.8 12h2.6" />
    <circle cx="12" cy="12" r="3.1" fill="currentColor" stroke="none" />
    <circle cx="12" cy="4" r="1.6" fill="currentColor" stroke="none" />
    <circle cx="20" cy="12" r="1.6" fill="currentColor" stroke="none" />
    <circle cx="12" cy="20" r="1.6" fill="currentColor" stroke="none" />
    <circle cx="4" cy="12" r="1.6" fill="currentColor" stroke="none" />
  </svg>
</template>

<style scoped>
.brand-glyph {
  display: block;
  flex: none;
}
</style>
