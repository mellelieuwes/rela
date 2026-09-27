<script setup lang="ts">
/**
 * A suggested replacement shown as removed and added text (TKT-S5C0K3).
 *
 * Both sides are MARKDOWN SOURCE rendered as plain text, never `v-html`: the
 * reviewer approves the exact characters that will land in the body, so the
 * diff must show those characters rather than what they would render to.
 */
defineProps<{
  /** The source text the suggestion replaces. */
  quote: string
  /** The proposed text; empty suggests deleting the quote. */
  replacement: string
}>()
</script>

<template>
  <div class="sgd" data-testid="suggestion-diff">
    <del class="sgd-old" aria-label="Current text">{{ quote }}</del>
    <ins v-if="replacement !== ''" class="sgd-new" aria-label="Suggested text">{{
      replacement
    }}</ins>
    <span v-else class="sgd-delete">Suggests deleting this text</span>
  </div>
</template>

<style scoped>
.sgd {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 0 0 6px;
  font-family: var(--font-mono, monospace);
  font-size: var(--font-size-sm);
}
.sgd-old,
.sgd-new {
  display: block;
  padding: 3px 7px;
  border-radius: var(--radius-sm, 5px);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.sgd-old {
  background: color-mix(in srgb, var(--error-color) 12%, transparent);
  color: var(--text-color);
}
.sgd-new {
  background: color-mix(in srgb, var(--success-color) 14%, transparent);
  color: var(--text-color);
  text-decoration: none;
}
.sgd-delete {
  color: var(--muted-text);
  font-family: inherit;
  font-style: italic;
}
</style>
