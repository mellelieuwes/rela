<script setup lang="ts">
import { useUIStore } from '@/stores'

const uiStore = useUIStore()

function getIcon(type: string): string {
  switch (type) {
    case 'success':
      return '✓'
    case 'error':
      return '✕'
    case 'warning':
      return '⚠'
    case 'info':
      return 'ℹ'
    default:
      return ''
  }
}
</script>

<template>
  <div class="toast-container">
    <TransitionGroup name="toast">
      <div
        v-for="toast in uiStore.toasts"
        :key="toast.id"
        class="toast"
        :class="toast.type"
        :data-testid="`toast-${toast.type}`"
      >
        <span class="toast-icon">{{ getIcon(toast.type) }}</span>
        <span class="toast-message">{{ toast.message }}</span>
        <button class="toast-dismiss" @click="uiStore.dismissToast(toast.id)">×</button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-container {
  position: fixed;
  bottom: 20px;
  right: 20px;
  z-index: 1000;
  display: flex;
  flex-direction: column;
  gap: 8px;
  /* The container spans its column but is not itself a target: it sits at
     bottom-right, directly over the form action buttons. That was harmless
     while every create navigated away, but "Create & add another"
     (TKT-7YHKD1) leaves the user on the form — and the toast reporting
     record N then swallowed the click for record N+1 until it timed out.
     Each toast re-enables pointer events for its own dismiss button. */
  pointer-events: none;
}

.toast {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  background: var(--toast-bg, #333);
  color: var(--toast-text, #fff);
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  min-width: 280px;
  max-width: 400px;
}

.toast.success {
  background: var(--success-color, #10b981);
}

.toast.error {
  background: var(--error-color, #ef4444);
}

.toast.warning {
  background: var(--warning-color, #f59e0b);
}

.toast.info {
  background: var(--info-color, #3b82f6);
}

.toast-icon {
  font-size: 18px;
}

.toast-message {
  flex: 1;
  font-size: 14px;
  /* Server messages can be multi-line (a config reload lists one error per line). */
  white-space: pre-line;
}

/* Only the dismiss control takes pointer events back. Re-enabling them on
   `.toast` itself was not enough of a scope: the toast's own icon and message
   spans then inherit `auto` and keep swallowing clicks aimed at what sits
   underneath — which, since "Create & add another" keeps the user on the form,
   is the very button they need to press for the next record. */
.toast-dismiss {
  pointer-events: auto;
  background: none;
  border: none;
  color: inherit;
  font-size: 20px;
  cursor: pointer;
  opacity: 0.7;
  padding: 0;
  line-height: 1;
}

.toast-dismiss:hover {
  opacity: 1;
}

.toast-enter-active,
.toast-leave-active {
  transition: all 0.3s ease;
}

.toast-enter-from {
  opacity: 0;
  transform: translateX(100%);
}

.toast-leave-to {
  opacity: 0;
  transform: translateX(100%);
}
</style>
