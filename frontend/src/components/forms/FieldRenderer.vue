<script setup lang="ts">
import { computed } from 'vue'
import type { FormFieldOrRelation, PropertyDef, AttachmentInfo, TransitionOption } from '@/types'
import { defaultRegistry, defaultWidgetFor } from '@/widgets/registry'
import FieldShell from './FieldShell.vue'
import StatusControl from './StatusControl.vue'

const props = defineProps<{
  field: FormFieldOrRelation
  propertyDef?: PropertyDef
  value: unknown
  error?: string
  readonly?: boolean
  // The server's explanation of a read-only verdict when it is more specific
  // than policy (`_fields[name].reason`, e.g. "owned by basecamp" for a field
  // an external system owns through a twin). Shown ahead of the field's help.
  readonlyReason?: string
  // Sparse per-option allow map: only `false` entries appear; absent
  // keys default to allowed. An option is disabled when EITHER this
  // map denies it or the existing transition rules deny it — the two
  // signals are independent and either one is sufficient.
  optionVerdicts?: Record<string, boolean>
  // Machine-aware status control (TKT-3G93B8): the server-resolved outgoing
  // transitions for a state-machine field. When present (and non-empty), the
  // field renders as a StatusControl showing only performable moves instead of
  // the ordinary enum widget. Undefined for a non-machine field → default
  // widget. An empty array means a machine field with no performable move (e.g.
  // a terminal state) — still routed to StatusControl so it shows the current
  // state without a picker, rather than a misleading full enum select.
  transitionOptions?: TransitionOption[]
  // File-widget context: the owning entity identity + current attachment
  // LIST + the property's `max`, forwarded so the file widget can
  // upload/preview/remove. Only the file-property edit path supplies these.
  entityType?: string
  entityId?: string
  attachments?: AttachmentInfo[]
  // Create-mode staged files for a `file` property (TKT-7K3BJF). Only the
  // file-property create path supplies these.
  stagedFiles?: File[]
  max?: number
}>()

const emit = defineEmits<{
  update: [value: unknown]
  'attachment-changed': []
  'update:staged-files': [files: File[]]
}>()

const fieldId = computed(() => `field-${props.field.property}`)
const label = computed(() => props.field.label || props.field.property || '')
const placeholder = computed(() => props.field.placeholder || '')
const help = computed(() => {
  const own = props.field.help || props.propertyDef?.description || ''
  // The server writes the reason as a finished sentence; shown verbatim.
  const reason = props.readonly && props.readonlyReason ? props.readonlyReason : ''
  return [reason, own].filter(Boolean).join(' · ')
})

// Resolve the widget once from config + property def. The registry
// honours an explicit field.widget then falls back to type defaulting.
const resolvedWidgetName = computed(() =>
  props.field.widget && props.field.widget.trim() !== ''
    ? props.field.widget
    : defaultWidgetFor(props.propertyDef)
)
const widgetComponent = computed(() =>
  defaultRegistry.resolve(props.field.widget, props.propertyDef)
)

const isCheckbox = computed(() => resolvedWidgetName.value === 'checkbox')

// A field is machine-aware when the server sent a `_transitions` entry for it
// (even an empty array — that's a terminal-state machine, still not a plain
// enum). Undefined means "not a state machine" and the field falls back to its
// ordinary widget. The current value is coerced to a string for the badge.
const isMachineField = computed(() => props.transitionOptions !== undefined)
const currentValue = computed(() => (props.value == null ? '' : String(props.value)))
</script>

<template>
  <FieldShell
    :field-id="fieldId"
    :label="label"
    :required="propertyDef?.required"
    :help="help"
    :error="error"
    :label-position="isCheckbox ? 'after' : 'before'"
    :span="field.span"
  >
    <StatusControl
      v-if="isMachineField"
      :model-value="currentValue"
      :property="field.property || ''"
      :entity-type="entityType"
      :transitions="transitionOptions || []"
      :disabled="readonly"
      @update:model-value="emit('update', $event)"
    />
    <component
      :is="widgetComponent"
      v-else
      :id="fieldId"
      :model-value="value"
      :mode="'edit'"
      :property-def="propertyDef"
      :property-name="field.property"
      :disabled="readonly"
      :required="propertyDef?.required"
      :error="error"
      :placeholder="placeholder"
      :help="help"
      :option-verdicts="optionVerdicts"
      :transitions="field.transitions"
      :attachments="attachments"
      :staged-files="stagedFiles"
      :max="max"
      :entity-type="entityType"
      :entity-id="entityId"
      @update:model-value="emit('update', $event)"
      @attachment-changed="emit('attachment-changed')"
      @update:staged-files="emit('update:staged-files', $event)"
    />
  </FieldShell>
</template>
