<script setup lang="ts">
/**
 * One twin of the entity, as a header chip: the external system, the sync
 * state (coloured), a link to the external item, and the last sync time as a
 * tooltip.
 *
 * ## A read-out, not a control
 *
 * Twins are linked, pulled and pushed only through `rela twin`; the web app
 * shows where an entity is mirrored and how that mirror stands. Nothing here
 * writes. Which fields the twin owns is not repeated on the chip — those
 * fields already render read-only with "owned by <system>" as their reason.
 *
 * ## The link is agent-supplied
 *
 * `url` was written by whoever linked the twin, so it is only rendered as a
 * link when it is http(s); anything else (a `javascript:` URL) shows the chip
 * without one rather than trusting it in an href.
 */
import { computed } from 'vue'
import type { Twin, TwinState } from '@/api/twins'

const props = defineProps<{ twin: Twin }>()

const STATE_LABELS: Record<TwinState, string> = {
  in_sync: 'in sync',
  pending: 'pending',
  conflict: 'conflict',
  gone: 'gone',
}

const stateLabel = computed(() => STATE_LABELS[props.twin.state] ?? props.twin.state)

const href = computed(() => (/^https?:\/\//i.test(props.twin.url) ? props.twin.url : undefined))

const tooltip = computed(() => {
  const synced = props.twin.synced_at
  const when = synced ? `last synced ${new Date(synced).toLocaleString()}` : 'never synced'
  return `${props.twin.system} ${props.twin.external_id}: ${when}`
})
</script>

<template>
  <component
    :is="href ? 'a' : 'span'"
    class="twin-badge"
    :class="`is-${twin.state}`"
    :href="href"
    :target="href ? '_blank' : undefined"
    :rel="href ? 'noopener' : undefined"
    :title="tooltip"
  >
    <span class="twin-badge__system">{{ twin.system }}</span>
    <span class="twin-badge__state">{{ stateLabel }}</span>
  </component>
</template>

<style scoped>
.twin-badge {
  display: inline-flex;
  gap: 0.3rem;
  align-items: center;
  padding: 0.05rem 0.4rem;
  margin-left: 0.35rem;
  font-size: 0.72rem;
  line-height: 1.5;
  vertical-align: middle;
  white-space: nowrap;
  text-decoration: none;
  border: 1px solid var(--twin-color);
  background: color-mix(in srgb, var(--twin-color) 16%, transparent);
  color: var(--text-color);
}

a.twin-badge:hover {
  background: color-mix(in srgb, var(--twin-color) 28%, transparent);
}

a.twin-badge:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.twin-badge__system {
  font-weight: 600;
}

.is-in_sync {
  --twin-color: var(--success-color);
}

.is-pending {
  --twin-color: var(--warning-color);
}

.is-conflict {
  --twin-color: var(--error-color);
}

.is-gone {
  --twin-color: var(--muted-text);
}
</style>
