<script setup>
// The mark is the Aria Compute favicon: a rounded-square badge carrying a
// stylised "A" — a diagonal stroke, a ring and a vertical stem. It takes its
// two inks from --brand-badge-bg / --brand-badge-fg (defined per theme in
// style.css), so the badge inverts between light and dark app themes and always
// pops off the surrounding surface.
defineProps({
  size: { type: [Number, String], default: 18 },
})
</script>

<template>
  <svg
    class="brand-glyph"
    :width="size"
    :height="size"
    viewBox="1 1 62 62"
    role="img"
    aria-label="Aria Compute"
  >
    <rect x="1" y="1" width="62" height="62" rx="18" class="brand-badge-bg" />
    <g class="brand-badge-fg" fill="none" stroke-linecap="round" stroke-linejoin="round">
      <path d="M12 53 23 11" stroke-width="8" />
      <circle cx="41" cy="37" r="11" stroke-width="6" />
      <path d="M51 54V27" stroke-width="6" />
    </g>
  </svg>
</template>

<style scoped>
.brand-glyph {
  display: block;
  flex: none;
}
.brand-badge-bg {
  fill: var(--brand-badge-bg);
}
.brand-badge-fg {
  stroke: var(--brand-badge-fg);
}
</style>
