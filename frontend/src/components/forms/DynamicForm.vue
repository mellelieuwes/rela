<script setup lang="ts">
import { ref, shallowRef, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute, onBeforeRouteLeave } from 'vue-router'
import { planPrefillRouting } from './prefillRouting'
import { useSchemaStore, useEntitiesStore, useUIStore } from '@/stores'
import { isCancelledFetch } from '@/composables/usePageData'
import { readReturnTo } from '@/utils/returnPath'
import { useWorld, DEFAULT_WORLD } from '@/composables/useWorld'
import { actionAllowed } from '@/utils/affordancesWarning'
import { entityRef, refBareId, refFace } from '@/utils/entityRef'
import { worldText } from '@/utils/worldText'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import {
  isFieldWritable,
  isPropertyRedacted,
  optionVerdictsFor as optionVerdictsForVerdict,
} from '@/utils/affordances'
import { isClearedForType } from '@/utils/formValue'
import { useEntityIDControls } from '@/composables/useEntityIDControls'
import { useConfirm } from '@/composables/useConfirm'
import { useHiddenFieldPolicy, clearWhenHiddenOf } from '@/composables/useHiddenFieldPolicy'
import { useChangePolicy } from '@/composables/useChangePolicy'
import type { ProposalOutcome } from '@/composables/useProposal'
import type {
  Entity,
  PropertyDef,
  FormFieldOrRelation,
  Template,
  ModernRelationsField,
  FieldAffordance,
  RelationAffordance,
  AttachmentInfo,
  TransitionOption,
  Mention,
} from '@/types'
import { getTemplates, createRelation, dryRunCreateEntity, ApiError, getErrorMessage } from '@/api'
import { uploadAttachment, attachmentErrorReason } from '@/api/attachments'
import type { RelationCardState } from './RelationCards.vue'
import type { RelationPickerIncomingState } from './RelationPicker.vue'
import {
  buildRelationsPatch,
  reshapeLegacyToModern,
  OUTGOING_SUFFIX,
  INCOMING_SUFFIX,
} from './relationsPatch'
import { ownedRelationKeys, filterOwnedRelations, untypedRelationKeys } from './ownedRelations'
import { useAutoSave } from '@/composables/useAutoSave'
import { useFormWizard } from '@/composables/useFormWizard'
import type { Bindings } from '@/utils/conditions'
import { registerForm } from './dirtyFormRegistry'
import { adoptLockedFieldValues } from './stagedEntity'
import AutoSaveIndicator from './AutoSaveIndicator.vue'
import FormFieldList from './FormFieldList.vue'
import MarkdownEditor from './milkdown/MilkdownEditor.vue'
import { makeRefResolver } from '@/utils/entityRefResolver'
import SidePanel from './SidePanel.vue'
import HelpModal from '@/components/ui/HelpModal.vue'
import PendingButton from '@/components/common/PendingButton.vue'
import { useDelayedPending } from '@/composables/useDelayedPending'
import { PENDING_TIMINGS } from '@/composables/pendingTimings'

const props = defineProps<{
  formId: string
  entityId?: string
  /**
   * Render as a nested form inside a host (the inline-create modal) rather
   * than as a page (TKT-OMUD56).
   *
   * A second mounted DynamicForm would otherwise fight the first over
   * process-global state: the document-level Cmd+Enter listener, the route
   * guard (whose `useConfirm` is a singleton that returns ONE user decision to
   * every concurrent caller), the `?step=` wizard query key, and the router
   * itself — a `router.push` on create unmounts the host form and destroys its
   * draft, which is the whole thing inline creation exists to avoid.
   *
   * So `embedded` suppresses every page-level side effect and replaces
   * navigation with `inline-created` / `inline-cancelled`. It is read at setup
   * time and never reactive afterwards.
   */
  embedded?: boolean
  /**
   * Pre-link and template context for an EMBEDDED form (TKT-R4BMJM).
   *
   * These exist because `embedded` deliberately reads an empty query (see
   * initializeDefaults): the form mounts over the host's page, so honouring
   * `route.query` would pre-fill the new entity from whatever that page was
   * showing and auto-link it to the host's peer. A host that genuinely has this
   * context passes it EXPLICITLY instead.
   *
   * They feed the same code paths the URL params feed, so pre-link and template
   * selection have one implementation with two entry points rather than two
   * implementations that can drift.
   *
   * Read at setup time and never reactive afterwards, matching `embedded`.
   */
  embeddedLink?: { relation: string; peer: string; linkAs: 'from' | 'to' }
  embeddedTemplate?: string
  embeddedWorld?: string
  /**
   * Values copied from a source entity for a Duplicate (TKT-Z8K2FS).
   *
   * Shaped like a template because that is what it is — a template computed
   * from an entity rather than read from `templates/` — so it flows through
   * `applyTemplate` and inherits its typed-value assignment and `originalData`
   * re-baseline instead of becoming a second apply path.
   *
   * Its keys are registered as user-touched. The commit filter keeps an
   * untouched key only while the dry-run reports it visible and writable, so a
   * prefilled value on a field the user never opens would otherwise be dropped
   * from the payload silently — the user would watch it sit in the form and
   * then find it missing from the copy. Marking them touched moves that
   * failure to the server's affordance gate, which refuses loudly with a
   * rule_id (RR-2U2D reasoning, RR-DDY9LG).
   *
   * Read at setup time and never reactive afterwards, matching `embedded`.
   */
  embeddedPrefill?: {
    properties: Record<string, unknown>
    content: string
    relations: Record<string, { id: string; type: string }[]>
  }
}>()

/**
 * Deliberately namespaced. Declaring emits removes these names from `$attrs`,
 * so a plain `created` / `cancel` would silently swallow a future mount's
 * native listener of the same name (RR-P3CO33).
 */
const emit = defineEmits<{
  'inline-created': [entity: Entity]
  'inline-cancelled': []
}>()

const router = useRouter()
const route = useRoute()
// The world the create button opened this form in — carried onto the
// post-create redirect so the user lands where the new face actually exists.
const { worldParam } = useWorld()
const schemaStore = useSchemaStore()
const entitiesStore = useEntitiesStore()
const uiStore = useUIStore()
const { confirm } = useConfirm()

// Link params for auto-linking after create (from custom views / side panels)
interface LinkParams {
  relation: string
  peer: string
  as: 'from' | 'to'
}
const linkParams = ref<LinkParams | null>(null)
// Relation keys a prefill channel seeded: `rel.<name>=<id>` query params and a
// Duplicate's copied relations. (`link_relation`/`link_peer` is the third and
// is read straight off `linkParams`.) Held at component scope because the
// ownership filter (BUG-KQSOJ2) must exempt them — none is constrained to a
// relation this form renders.
//
// Exempting them is the point: a prefilled edge is the caller's explicit
// instruction, so dropping it writes nothing and says nothing. That is the
// silent no-write the post-condition check in handleSubmit exists to catch,
// and for Duplicate it would quietly narrow what "duplicate" copies.
const prefilledRelations = ref<Set<string>>(new Set())
const returnTo = ref<string | null>(null)

// State
const formData = ref<Record<string, unknown>>({})
const relations = ref<Record<string, string[]>>({})
// Per-entity field affordances from the server. Loaded together with
// the entity in edit mode; populated as `_fields` from the API. Drives
// readonly + option-filter rendering and the F1 hidden-field filter
// (TKT-G7N5).
const fieldAffordances = ref<Record<string, FieldAffordance>>({})
// Property names the server withheld by field-level ACL (`_redacted`,
// DEC-T0XIWQ). This is the edit-mode hidden-field signal: previously the
// filter inferred hiding from a key's absence in `properties`, which also
// matches a property that was simply never set — so every unset property
// became permanently unreachable (BUG-MLT9DE). Empty = nothing redacted.
const redactedProps = ref<string[]>([])
// Same for relation affordances. Drives RelationCards' +Add / x button
// visibility and meta-field disable.
const relationAffordances = ref<Record<string, RelationAffordance>>({})
// Per-`file`-property attachment metadata from the loaded entity, passed
// to the file widget so it can show the current file + drive upload/remove.
const attachments = ref<Record<string, AttachmentInfo[]>>({})
// TKT-7K3BJF: files picked in CREATE mode, not yet uploaded. An attachment
// cannot be written before the entity row exists (every store backend returns
// ErrNotFound), and there is no id-reservation primitive, so the only possible
// order is create-then-attach. These are uploaded in handleSubmit once the
// server returns an id.
//
// They are deliberately NOT part of `formData`: `payload.properties` is built
// from it, so a File (or a placeholder path) sitting there would be POSTed as
// the file property's value at create. The server stamps that property itself
// once the bytes land.
// shallowRef, not ref (RR-C1MI2N): a deep ref proxies its contents, and
// whether a File escapes that depends on the environment — happy-dom's File
// reports `[object Object]` and IS proxied (identity not preserved), while a
// spec File reports `File` and is left alone. That divergence would mean the
// unit tests exercise proxies while production exercises raw Files, silently
// changing identity comparisons and the brand checks in URL.createObjectURL /
// FormData.append. Nothing here needs deep reactivity: updateStagedFiles
// replaces the whole object on every change.
const stagedFiles = shallowRef<Record<string, File[]>>({})

// Set once a create has SUCCEEDED, so a later submit from the same mounted
// form cannot mint a second entity (RR-4QO887). The happy path navigates away
// immediately, but navigation is asynchronous and the failure path stays on a
// live form long enough for a second Create click or Cmd+Enter to land. The
// `saving` guard only covers an in-flight submit; this covers a completed one.
const createdEntityId = ref<string | null>(null)

function updateStagedFiles(property: string, files: File[]) {
  if (files.length) stagedFiles.value = { ...stagedFiles.value, [property]: files }
  else {
    const next = { ...stagedFiles.value }
    delete next[property]
    stagedFiles.value = next
  }
  // Clear a standing validation error once a file is staged, mirroring what
  // updateField does as the user edits an ordinary field (BUG-L1DHC5) — else a
  // "This field is required" flag would persist after the user has addressed
  // it. A full re-check still runs on submit.
  if (files.length && errors.value[property]) {
    const next = { ...errors.value }
    delete next[property]
    errors.value = next
  }
  checkDirty()
}

function hasStagedFiles(): boolean {
  return Object.values(stagedFiles.value).some((list) => list.length > 0)
}
// Per-state-machine-field transition verdicts from the loaded entity
// (`_transitions`, TKT-3G93B8). Present only for machine-typed fields; drives
// the StatusControl (only-allowed moves) instead of the plain enum select. A
// field absent here renders as an ordinary widget.
const transitions = ref<Record<string, TransitionOption[]>>({})
// TKT-3I5U: in create mode the form models a staged `++new++` entity
// and re-derives affordances from the server's dry-run (no persist) as
// the user types. `stagedVisibleProps` holds the property keys the
// dry-run returned as visible (hidden fields are stripped server-side),
// so the field filter can hide policy-hidden fields in create mode the
// same way edit mode uses the loaded entity's `properties`. Empty until
// the first dry-run resolves.
const stagedVisibleProps = ref<Set<string>>(new Set())
// True once at least one create-mode dry-run has resolved, so the field
// filter switches from "render everything" (first paint, F19) to the
// affordance-filtered list. Stays false in edit mode.
const stagedAffordancesReady = ref(false)
// Property keys the user has explicitly edited via updateField in
// create mode. The commit-side filter always preserves these keys even
// when the dry-run hasn't reported them as visible-writable yet (debounce
// race, RR-2U2D), and they're authoritative over a stale metamodel
// default for "what was the user's intent" — the server's gate (BUG-Q60V)
// is the boundary that rejects a touched key the policy denies.
const userTouched = ref<Set<string>>(new Set())
// Per-relation `id -> entity type` map, fed by RelationPicker's
// `update:types` emit. Required by the unified PATCH builder to emit
// JSON:API §9 resource identifiers without guessing target types
// via `to[0]` (which is wrong for polymorphic relations).
const pickerTypes = ref<Record<string, Map<string, string>>>({})
const content = ref('')
// False when the server reports `content_writable: false` (an external system
// owns the body through a twin): the editor renders read-only with a hint. Any
// other value — including absent, on a create — leaves it editable; the server
// re-enforces ownership on the write either way.
const contentWritable = ref(true)
// The server's per-principal mentions map for the loaded body, and the
// resolver the editor renders references with. Shared with the read view via
// makeRefResolver so a reference shows the same title on both surfaces.
const mentions = ref<Record<string, Mention> | undefined>(undefined)
const refResolver = computed(() => makeRefResolver(mentions.value))
const loading = ref(true)
// Opening an edit form against a local server resolves in tens of
// milliseconds, so an ungated spinner appears and vanishes before it can be
// read while the form springs into place underneath. There is no previous
// content to hold here (a form is either opening or it is not), so the
// delay alone is the fix: below it, nothing is shown.
const showBlockLoader = useDelayedPending(() => loading.value, {
  delay: PENDING_TIMINGS.navDelayMs,
  minDuration: PENDING_TIMINGS.navMinDurationMs,
})
// loadState is a stable, test-visible signal of whether the edit entity
// actually loaded: 'pending' until the fetch settles, then 'loaded' or
// 'error'. Stamped as data-testid on the form root so an out-of-process
// consumer (the rela-docs screenshot harness) can unambiguously tell a
// rendered-with-data form from an empty schema-only shell after a failed load.
const loadState = ref<'pending' | 'loaded' | 'error'>('pending')
// Set to true in edit mode when the loaded entity's `_actions.update`
// is explicitly false. The template renders an inline "not editable"
// message instead of the form. The EntityDetail Edit button already
// gates on the same verdict, so this branch only fires for direct-URL
// navigation (bookmark, paste) or when the policy tightened after the
// detail view loaded.
const notEditable = ref(false)
// The NON-BARE face the loaded row is, or '' — so the refusal can carry the
// operator's `faces.<name>.messages.read_only` when one is declared. Nothing
// declared: the ordinary permissions text, which is true either way and
// speaks no rela vocabulary (TKT-5SZG2L).
const notEditableFace = ref('')
// The loaded row's display title, so `{title}` reads the same here as on
// the detail page — the same declared sentence must not name the entity
// one way there and by its id here.
const notEditableTitle = ref('')
const notEditableNote = computed(() => {
  const type = formConfig.value?.entity ?? ''
  const faces = schemaStore.getEntityType(type)?.faces
  const face = notEditableFace.value
  if (!face) return ''
  return worldText(faces?.[face]?.messages?.read_only, {
    face: faces?.[face]?.label || face,
    world: worldParam.value ?? '',
    title: notEditableTitle.value,
  })
})
const saveGeneration = ref(0) // Incremented after save to reset RelationCards
const saving = ref(false)
const dirty = ref(false)
const errors = ref<Record<string, string>>({})
const originalData = ref<string>('')
// TKT-7YHKD1: polite live region for "Create & add another". Everything else
// about that action is visual — the form blanks, a toast appears — and the
// form's only other live region is the error summary, which is gated on there
// being errors. Repeated keyboard entry is precisely the workflow an AT user
// benefits from most, so silence here would be the wrong default.
const statusAnnouncement = ref('')
function announce(message: string) {
  // Clear first so an identical consecutive message still re-announces.
  statusAnnouncement.value = ''
  nextTick(() => {
    statusAnnouncement.value = message
  })
}
const helpModalOpen = ref(false)
const templates = ref<Template[]>([])
const selectedTemplate = ref<string>('')

// Computed
const formConfig = computed(() => schemaStore.getForm(props.formId))
const entityType = computed(() => {
  if (!formConfig.value) return undefined
  return schemaStore.getEntityType(formConfig.value.entity)
})

const isEdit = computed(() => !!props.entityId)

/** The entity whose body the editor holds; the `@` menu starts from it. */
const mentionSelf = computed(() =>
  props.entityId && formConfig.value
    ? {
        id: refBareId(props.entityId),
        type: formConfig.value.entity,
        face: refFace(props.entityId),
      }
    : undefined
)
const formMode = computed(() => (isEdit.value ? 'edit' : 'create') as 'create' | 'edit')

const idControls = useEntityIDControls(entityType, formMode)
const { showManualIDInput, showPrefixPicker, prefixOptions, manualId, selectedPrefix } = idControls

const showReadOnlyID = computed(() => isEdit.value && entityType.value?.id_type === 'manual')

const title = computed(() => {
  if (!formConfig.value) return ''
  const label = entityType.value?.label || formConfig.value.entity
  return isEdit.value ? `Edit ${label}` : `New ${label}`
})

const allFields = computed((): FormFieldOrRelation[] => {
  if (!formConfig.value) return []
  // Wizard forms carry their fields under steps; flatten them so affordance
  // filtering, payload assembly, and load-time hydration see every field the
  // same way single-page forms do. Per-step visibility is applied separately
  // by the wizard layer at render/validate time.
  if (formConfig.value.steps?.length) {
    return formConfig.value.steps.flatMap((s) => [
      ...((s.fields || []) as FormFieldOrRelation[]),
      ...((s.relations || []) as FormFieldOrRelation[]),
    ])
  }
  // Combine property fields and relation fields into a single list
  const propFields = (formConfig.value.fields || []) as FormFieldOrRelation[]
  const relFields = (formConfig.value.relations || []) as FormFieldOrRelation[]
  return [...propFields, ...relFields]
})

// Live binding namespaces for condition evaluation. Supplied as a getter (not a
// computed) so the reactive read of formData happens inside each wizard
// computed's tracking scope — see useFormWizard. `entity`/`current_user` are
// reserved for future ACL/view use (form conditions reference `form.<field>`).
const conditionBindings = (): Bindings => ({
  form: formData.value,
  entity: {},
  current_user: {},
})

// An embedded wizard keeps its step in memory: `?step=` is a single global key
// that the host form already owns (see FormWizardOptions.syncUrl).
const wizard = useFormWizard(formConfig, conditionBindings, { syncUrl: !props.embedded })

// Field lookup by property, for reading per-field config (clear_when_hidden)
// without re-scanning allFields at every call site.
const fieldByProperty = computed(() => {
  const map = new Map<string, FormFieldOrRelation>()
  for (const f of allFields.value) {
    if (f.property) map.set(f.property, f)
  }
  return map
})

// BUG-FB0LN8: the fate of a condition-hidden field's STORED value. Default is
// to keep it — hiding is presentation, not a delete. See useHiddenFieldPolicy.
const hiddenPolicy = useHiddenFieldPolicy({
  policyFor: (property) => clearWhenHiddenOf(fieldByProperty.value.get(property)),
})

// Visible-step indices that currently have a validation error, so the stepper
// can flag them. Recomputes as `errors` changes, so a pill's flag clears the
// moment its field becomes valid.
const stepsWithErrors = computed<Set<number>>(() => {
  const flagged = new Set<number>()
  for (const property of Object.keys(errors.value)) {
    const idx = wizard.visibleStepIndexForProperty(property)
    if (idx >= 0) flagged.add(idx)
  }
  return flagged
})

const errorCount = computed(() => Object.keys(errors.value).length)

// React to a conditional field/step becoming hidden or revealed. TWO effects,
// both NON-DESTRUCTIVE:
//   1. Hiding: drop any standing validation error for a now-hidden field, so it
//      can't leave a phantom "N fields need attention" with no flagged,
//      reachable step (RR-U9ERK).
//   2. Revealing: restore the retained value, so hide → reveal is lossless and
//      needs no server round-trip.
//
// TKT-7S5735: the destructive effect (retain-then-`clear_when_hidden`) used to
// live here too; it moved into `useChangePolicy`, which runs BEFORE the
// mutation. This watcher is post-flush — it fires *because* `formData` already
// changed — so it can observe a hide but never gate one.
//
// Skips the initial hydration (`loading`); only touches wizard-governed
// (managed) fields, so a plain non-conditional field is never affected.
watch(
  () => wizard.activeProperties.value,
  (active, prevActive) => {
    if (loading.value || !prevActive) return
    const managed = wizard.managedProperties.value
    let nextErrors: Record<string, string> | null = null

    // Reveal: put back what we held while the branch was hidden.
    for (const prop of active) {
      if (prevActive.has(prop) || !managed.has(prop)) continue
      if (!hiddenPolicy.hasRetained(prop)) continue
      if (!isClearedForType(formData.value[prop], entityType.value?.properties?.[prop])) continue
      formData.value[prop] = hiddenPolicy.retainedValue(prop)
      hiddenPolicy.release(prop)
    }

    // Hiding: clear standing errors only. The value's fate was already decided
    // in `proposeChange`, before the change that got us here was applied.
    for (const prop of prevActive) {
      if (active.has(prop) || !managed.has(prop)) continue // still shown / not governed
      if (errors.value[prop]) {
        nextErrors ??= { ...errors.value }
        delete nextErrors[prop]
      }
    }
    if (nextErrors) errors.value = nextErrors
  }
)

// Apply `clear_when_hidden` to the fields that just hid. Called by the change
// policy AFTER it has retained their pre-change values, so a reveal is lossless
// whatever the policy says; only the server-side clear is per-field.
function applyHidePolicy(toClear: string[]) {
  if (!autoSave.value) {
    // No write channel yet (the brief window before onMounted wires autosave).
    // Retention already stands, so nothing is lost — but a clear the user
    // explicitly approved would silently not happen. Drop the retained copy so
    // the field does not later re-appear holding a value they asked to remove.
    for (const prop of toClear) hiddenPolicy.release(prop)
    return
  }

  // `toClear` is already the DECIDED set — the change policy resolved
  // `clear_when_hidden` (including any dialog answer) before calling. Do not
  // re-filter it here.
  for (const prop of toClear) {
    delete formData.value[prop]
    hiddenPolicy.release(prop)
    autoSave.value.scheduleUnset(prop)
  }
}

// TKT-G7N5 F1 / TKT-3I5U / DEC-T0XIWQ: filter the config-driven field list
// against the entity's affordances. Both render paths (this and the wizard's
// `visibleStepFields`) delegate to the single `affordanceVisible` predicate
// below — two hand-synced copies of this rule is how the wizard path silently
// carried BUG-MLT9DE alongside the flat one.
const fields = computed((): FormFieldOrRelation[] => allFields.value.filter(affordanceVisible))

// TKT-G7N5 readonly helper: the rendered field is readonly if either
// the config marks it so OR the server's _fields verdict reports
// writable=false. Both sources are honored — the server's affordance
// is the strongest signal but the config can still set its own
// readonly for static cases (e.g. ID display).
function isFieldReadonly(field: FormFieldOrRelation): boolean {
  if (!field.property) return field.readonly === true
  const verdict = fieldAffordances.value[field.property]
  return !isFieldWritable(verdict, field.readonly)
}

// The server's reason for a read-only verdict (e.g. "owned by basecamp"),
// shown as the field's help. Undefined when none was given.
function readonlyReasonFor(field: FormFieldOrRelation): string | undefined {
  if (!field.property) return undefined
  return fieldAffordances.value[field.property]?.reason
}

// TKT-G7N5 option-verdict helper: pulls per-option allowed-map from
// the server's _fields verdict. Undefined if no verdict for this
// field (all options allowed by default). Sparse: only false entries
// appear in the map.
function optionVerdictsFor(field: FormFieldOrRelation): Record<string, boolean> | undefined {
  if (!field.property) return undefined
  return optionVerdictsForVerdict(fieldAffordances.value[field.property])
}

// TKT-3G93B8 transition helper: the server-resolved outgoing transitions for a
// state-machine field (`_transitions`). Undefined for a non-machine field, so
// FieldRenderer renders the ordinary widget; a machine field routes to the
// StatusControl. Only present in edit mode (a create locks the field instead —
// see applyCreateLock on the backend).
function transitionsFor(field: FormFieldOrRelation): TransitionOption[] | undefined {
  if (!field.property) return undefined
  return transitions.value[field.property]
}

// TKT-3I5U: build the create-commit property map, sending ONLY visible
// and writable keys. Hidden fields (stripped from the staged dry-run's
// visible set) and read-only fields (writable=false in `_fields`) are
// omitted so the server applies their defaults itself, downstream of
// the affordance gate (RR-SIA6). Without affordances (fail-open) we
// send everything — the commit gate is the boundary that rejects any
// denied write.
function visibleWritablePropertiesForCommit(): Record<string, unknown> {
  if (isEdit.value || !stagedAffordancesReady.value) {
    return { ...formData.value }
  }
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(formData.value)) {
    // RR-2U2D: never drop a key the user explicitly typed into — the
    // dry-run may not have resolved yet for that key (debounce in
    // flight) and silently dropping the value would lose user intent.
    // The server's affordance gate (BUG-Q60V) rejects denied writes,
    // so this is safe: an under-resolved touched key that the policy
    // actually denies still 403s at commit, with a clear rule_id.
    if (userTouched.value.has(key)) {
      out[key] = value
      continue
    }
    if (!stagedVisibleProps.value.has(key)) continue // hidden → omit
    if (fieldAffordances.value[key]?.writable === false) continue // read-only → omit
    out[key] = value
  }
  return out
}

// Helper to look up entity type from ID prefix (e.g., "TKT-001" -> "ticket")
function getTypeFromId(entityId: string): string | undefined {
  const prefix = entityId.split('-')[0]
  if (!prefix) return undefined

  for (const [typeName, typeDef] of schemaStore.entityTypes) {
    if (typeDef.id_prefix?.toUpperCase() === prefix.toUpperCase()) {
      return typeName
    }
  }
  return undefined
}

// Methods
async function loadEntity(force = false) {
  if (!props.entityId || !formConfig.value) return

  try {
    // The entity id is an ADDRESS — `POL-1` or `POL-1@published` — and the
    // form edits exactly the row it names: what you look at is what you edit
    // is what you save. So the fetch is pinned to the DEFAULT world, where
    // every address is literal. Fetching in the ambient world instead let a
    // configured `default_world` resolve a bare id AWAY from the row the
    // Edit button was pressed on, and the form then refused the served face
    // "for permissions" (atlas worlds issue 7).
    const entity = await entitiesStore.fetchEntity(
      formConfig.value.entity,
      props.entityId,
      force,
      DEFAULT_WORLD
    )
    // Route-guard: if the server says this row is not updatable, render an
    // inline "not editable" message instead of the form. The EntityDetail
    // Edit button already hides for the same verdict, so this branch fires
    // only for direct-URL navigation.
    notEditable.value = !actionAllowed(entity, 'update')
    notEditableFace.value = refFace(entityRef(entity))
    notEditableTitle.value = entityDisplayTitle(entity)
    // Retained hidden values belong to the form state we are about to replace.
    // Carrying them across a reload — or across an entity switch, since this
    // component is not re-keyed per entity — would restore one entity's value
    // onto another, or resurrect a value the server no longer has (BUG-FB0LN8).
    // The stored value is safe on the server, so dropping the cache is lossless:
    // a later reveal reads it back from `properties`.
    hiddenPolicy.releaseAll()
    formGeneration.value++
    // Take the autosave merge base HERE — this is the one moment we hold
    // unmodified server state, before the spreads below hand it to the form
    // (TKT-DPBQ7S). Taken before mutation, and cloned, so the baseline can
    // never alias the form's working copy.
    recordServerBaseline(entity)
    formData.value = { ...entity.properties }
    relations.value = entity.relations ? { ...entity.relations } : {}
    content.value = entity.content || ''
    contentWritable.value = entity.content_writable !== false
    // TKT-G7N5: per-entity affordances from the server. The wire keys
    // are always present on per-entity GET (possibly empty); we
    // default to empty maps so the filter / readonly / options paths
    // can treat absence as "default everything".
    fieldAffordances.value = entity._fields ?? {}
    redactedProps.value = entity._redacted ?? []
    relationAffordances.value = entity._relations ?? {}
    attachments.value = entity._attachments ?? {}
    transitions.value = entity._transitions ?? {}
    // Entity-ID code spans in the body, resolved per principal by the server.
    // The editor renders references as titled links from this and nothing
    // else; absent means every reference shows its bare ID, which is the
    // correct degraded state rather than an error.
    mentions.value = entity.mentions
    originalData.value = JSON.stringify({
      formData: formData.value,
      relations: relations.value,
      content: content.value,
    })
    loadState.value = 'loaded'
  } catch (err) {
    // Suppress cancellation errors from rapid navigation in Firefox
    // (see BUG-6C3V and src/composables/usePageData.ts).
    if (isCancelledFetch(err)) return
    loadState.value = 'error'
    uiStore.error('Failed to load entity')
    console.error(err)
  }
}

// uploadStagedFiles pushes every create-mode staged file to the attachment
// endpoint against the just-created id, and returns the failures.
//
// Continue-on-error (RR-Z7C3CY): every file is attempted even after one is
// rejected, so the user is told about all of them at once rather than
// discovering the rest on a later save. Sequential rather than parallel: the
// server serializes attachment writes on its write mutex anyway, and
// WriteAttachment's capacity check is read-then-write, which concurrent
// uploads to one property would race.
async function uploadStagedFiles(
  entityType: string,
  entityId: string
): Promise<{ file: string; message: string }[]> {
  const failures: { file: string; message: string }[] = []
  // RR-OI6P51: apply the same wizard pruning the property payload gets
  // (pruneWizardHidden). Without it, staging a file on a step the user then
  // hides uploads the bytes anyway and the SERVER stamps the property —
  // persisting a revealed-then-hidden branch, which is exactly what TKT-CHLAJ
  // prevents for ordinary properties.
  const active = wizard.activeProperties.value
  const managed = wizard.managedProperties.value
  for (const [property, files] of Object.entries(stagedFiles.value)) {
    if (managed.has(property) && !active.has(property)) continue
    for (const file of files) {
      try {
        await uploadAttachment(entityType, entityId, property, file)
      } catch (err) {
        failures.push({ file: file.name, message: attachmentErrorReason(err) })
      }
    }
  }
  return failures
}

// One message naming every file that failed, so "which of my files made it?"
// is answerable without comparing lists by hand.
function stagedFailureMessage(failures: { file: string; message: string }[]): string {
  const detail = failures.map((f) => `${f.file} (${f.message})`).join(', ')
  const plural = failures.length > 1 ? 's' : ''
  // Names the files AND says where to fix it: the staged copies are gone once
  // this form unmounts (RR-4QO887), so "re-attach on the entity" is the only
  // honest instruction.
  return `Entity created, but ${failures.length} file${plural} failed to attach: ${detail}. Re-attach on the entity.`
}

// onAttachmentChanged fires after a file widget uploads or removes an
// attachment (already persisted server-side via the attachment endpoint).
// Reload the entity so the stamped property value and _attachments
// refresh. Cache-bust the entity first so the SSE-less refetch is fresh.
async function onAttachmentChanged() {
  await loadEntity(true)
}

// Read return_to from the query eagerly — needed in both create and
// edit modes. initializeDefaults below handles create-only pre-fills
// (prop.*, rel.*, link_*) and early-returns in edit mode, so return_to
// can't live in there if edit submits are to honour it too.
//
// readReturnTo from utils enforces the open-redirect guard and the
// array-valued-query case (vue-router yields string[] on duplicate keys).
function applyReturnToFromQuery() {
  const safe = readReturnTo(route.query)
  if (safe) returnTo.value = safe
}

function initializeDefaults() {
  if (!entityType.value || isEdit.value) return

  idControls.reset()

  // Parse query params for pre-filling (prop.*, rel.*, link_*).
  //
  // Embedded forms read an EMPTY query: they are mounted over whatever page
  // the host is on, so `route.query` belongs to that page. Honouring it would
  // pre-fill the nested entity from the host's parameters and — via link_* —
  // auto-link it to the host's peer, both silently wrong. Field defaults and
  // templates below still apply; only the URL-sourced overlay is dropped.
  const query = props.embedded ? {} : route.query
  const queryProps: Record<string, string> = {}
  const queryRels: Record<string, string[]> = {}
  // Rebuilt from the query below, not accumulated: this runs again on a
  // template switch and on add-another, both of which reset `relations.value`.
  // Keeping a key whose edge was just cleared would exempt a relation that is
  // no longer prefilled.
  prefilledRelations.value = new Set()

  for (const [key, value] of Object.entries(query)) {
    if (typeof value !== 'string') continue

    if (key.startsWith('prop.')) {
      const propName = key.slice(5) // Remove 'prop.' prefix
      queryProps[propName] = value
    } else if (key.startsWith('rel.')) {
      const relType = key.slice(4) // Remove 'rel.' prefix
      if (!queryRels[relType]) {
        queryRels[relType] = []
      }
      queryRels[relType].push(value)
    } else if (key === 'link_relation' && typeof query.link_peer === 'string') {
      linkParams.value = {
        relation: value,
        peer: query.link_peer,
        as: (query.link_as as 'from' | 'to') || 'to',
      }
    }
  }

  // The embedded host's pre-link, feeding the SAME linkParams the URL feeds.
  // An embedded form read an empty query above, so this is the only way the
  // context reaches it — and routing it here means the downstream handling
  // (the `to` edge in the payload, the `from` edge after create) has one
  // implementation rather than an embedded copy.
  if (props.embedded && props.embeddedLink) {
    linkParams.value = {
      relation: props.embeddedLink.relation,
      peer: props.embeddedLink.peer,
      as: props.embeddedLink.linkAs,
    }
  }

  // Apply metamodel defaults
  for (const [propName, propDef] of Object.entries(entityType.value.properties)) {
    if (propDef.default !== undefined) {
      formData.value[propName] = propDef.default
    }
  }

  // Apply form-level defaults. Iterate `allFields.value` directly
  // (not the affordance-filtered `fields` computed): defaults must seed
  // from every configured field, independent of any incidental
  // affordance state. RR-00VT made this dependency explicit so a
  // future change to `fields`'s early-return can't silently drop
  // create-mode defaults.
  for (const field of allFields.value) {
    if (field.property && field.default !== undefined) {
      formData.value[field.property] = field.default
    }
    if (field.relation && field.default !== undefined) {
      const defaultValue = field.default
      if (Array.isArray(defaultValue)) {
        relations.value[field.relation] = defaultValue as string[]
      } else {
        relations.value[field.relation] = [defaultValue as string]
      }
    }
  }

  // Apply query param overrides (highest priority)
  for (const [propName, value] of Object.entries(queryProps)) {
    formData.value[propName] = value
  }
  for (const [relType, targets] of Object.entries(queryRels)) {
    if (!relations.value[relType]) {
      relations.value[relType] = []
    }
    // Record the key so the ownership filter exempts it (BUG-KQSOJ2). Done
    // even when the form renders the relation: a rendered picker types it and
    // owns it anyway, so the exemption is redundant there rather than wrong,
    // and tracking every key keeps the rule "a prefill is always carried".
    prefilledRelations.value.add(relType)
    for (const target of targets) {
      if (!relations.value[relType].includes(target)) {
        relations.value[relType].push(target)
      }
    }
  }

  // Pre-fill the relation from link params.
  //
  // ONLY for `link_as=to`, where the new entity is the relation's TO: the
  // create payload expresses `new --relation--> peer`, which is exactly that
  // edge. For `link_as=from` the edge runs the other way and the payload cannot
  // say so, so it is created by the second call after submit — putting the peer
  // in the payload there would write a BACKWARDS edge in addition to the
  // correct one.
  if (linkParams.value && linkParams.value.as === 'to') {
    const rel = linkParams.value.relation
    const peer = linkParams.value.peer
    if (!relations.value[rel]) {
      relations.value[rel] = []
    }
    if (!relations.value[rel].includes(peer)) {
      relations.value[rel].push(peer)
    }
    // Register the peer's TYPE as well as its id (BUG found in TKT-R4BMJM).
    //
    // `pickerTypes` is otherwise populated only by RelationPicker, via
    // updateRelationTypes. A pre-linked relation that has no picker field on
    // this form therefore had no type entry — and reshapeLegacyToModern returns
    // null for an id it cannot type, which aborts the WHOLE create with
    // "Some related entities have unknown types".
    //
    // So a create button for a relation the form does not also render as a
    // field could not save at all.
    //
    // A prefix-less peer id (the demo project's categories are `backend`,
    // `devops`) yields no type here. That is survivable on THIS path and only
    // here: a form that renders the relation as a picker has already registered
    // the type, and one that does not will fail loudly at submit rather than
    // write a mistyped edge.
    const peerType = getTypeFromId(peer)
    if (peerType) {
      const types = pickerTypes.value[rel] ?? new Map<string, string>()
      types.set(peer, peerType)
      pickerTypes.value[rel] = types
    }
  }

  originalData.value = JSON.stringify({
    formData: formData.value,
    relations: relations.value,
    content: content.value,
  })
}

/**
 * The template variant this form was asked to open with, if any.
 *
 * Two channels for one meaning: a prop when embedded (an embedded form reads no
 * query), a `?template=` param on the page flow. Neither is authoritative over
 * the other because a form is only ever one of the two.
 */
function requestedTemplate(): string | undefined {
  if (props.embedded) return props.embeddedTemplate
  const q = route.query.template
  return typeof q === 'string' && q !== '' ? q : undefined
}

async function loadTemplates() {
  if (!formConfig.value) return
  try {
    templates.value = await getTemplates(formConfig.value.entity)
    if (templates.value.length > 0) {
      // An operator-preselected variant wins over templates[0] (TKT-R4BMJM).
      // It arrives as a prop when embedded and as `?template=` on the page
      // flow, so both create surfaces resolve through this one selection.
      //
      // A name that matches nothing falls back to the default rather than
      // erroring: the variant may have been deleted from disk since config
      // load, and refusing to open the form would be a worse answer than
      // opening it with the ordinary default.
      const requested = requestedTemplate()
      const chosen =
        (requested && templates.value.find((t) => t.name === requested)) || templates.value[0]
      // Select first template by default. This lands after the form is
      // already interactive, so it must not overwrite what the user has
      // begun writing.
      selectedTemplate.value = chosen.name
      applyTemplate(chosen, true)
    }
  } catch (err) {
    // Templates are optional, ignore errors
    console.warn('Failed to load templates:', err)
  }
}

// TKT-3I5U create-mode affordance machinery.
//
// The staged entity's verdicts depend on its current field VALUES
// (value-dependent predicates), so we re-derive them via the server's
// dry-run create (no persist) on mount and, debounced, as the user
// types. Verdicts are ADVISORY — the real create re-authorizes; on any
// dry-run failure we fail OPEN (leave the form unfiltered/usable),
// since a missing hint is a UX regression, not a security hole
// (RR-HUQ3). Only the latest request's result is applied (RR-ZKL2).
const STAGED_DRYRUN_DEBOUNCE_MS = 400
let stagedDryRunController: AbortController | null = null
let stagedDryRunTimer: ReturnType<typeof setTimeout> | null = null
// RR-2PZB: signals that the component is gone so a dry-run response
// arriving post-unmount doesn't write to refs of a destroyed component.
let stagedUnmounted = false

async function refreshStagedAffordances() {
  if (isEdit.value || !formConfig.value) return

  // Drop any in-flight request: only the newest form state matters.
  stagedDryRunController?.abort()
  const controller = new AbortController()
  stagedDryRunController = controller

  try {
    const candidate = await dryRunCreateEntity(
      formConfig.value.entity,
      {
        properties: { ...formData.value },
        content: content.value || undefined,
        // Same face the submit will write, so the verdict matches the create.
        world: worldParam.value || undefined,
      },
      controller.signal
    )
    // A newer request superseded this one between await points — discard.
    if (controller !== stagedDryRunController) return
    // Component is gone (unmount fired between resolve and here) — bail
    // so we don't write to dead refs (RR-2PZB).
    if (stagedUnmounted) return

    fieldAffordances.value = candidate._fields ?? {}
    relationAffordances.value = candidate._relations ?? {}
    // Entry-locked create fields (TKT-3G93B8 / BUG-X1C7S): adopt the server's
    // authoritative value for every read-only field (a machine field the server
    // pinned to its initial value, or any policy-read-only field) so the locked
    // control DISPLAYS the server value, not stale user input. Scoped to read-only
    // fields, so it never clobbers an editable one. See adoptLockedFieldValues.
    adoptLockedFieldValues(candidate._fields, candidate.properties, formData.value)
    // The dry-run strips hidden fields from `properties`; the remaining
    // keys are exactly the visible-by-default props the field filter
    // needs to render (since they won't appear in the sparse `_fields`).
    stagedVisibleProps.value = new Set(Object.keys(candidate.properties ?? {}))
    // Note: dry-run soft warnings are intentionally NOT surfaced as
    // toasts here — nothing is saved, and a per-keystroke "Saved with
    // warnings" toast would be noisy and misleading. Warnings still
    // surface on the real commit response. Inline per-field validation
    // feedback from the dry-run is a future enhancement.
    stagedAffordancesReady.value = true
  } catch (err) {
    if (isCancelledFetch(err)) return // superseded / unmounted — not an error
    if (stagedUnmounted) return // post-unmount; don't write to dead refs
    // Fail open: leave whatever affordances we have (possibly none) and
    // let the form render. The commit gate is the real boundary.
    console.warn('Dry-run affordance check failed; create form left unfiltered:', err)
    stagedAffordancesReady.value = true
  }
}

// scheduleStagedAffordances debounces refreshStagedAffordances so a
// burst of keystrokes collapses to one dry-run.
function scheduleStagedAffordances() {
  if (isEdit.value) return
  if (stagedDryRunTimer) clearTimeout(stagedDryRunTimer)
  stagedDryRunTimer = setTimeout(() => {
    void refreshStagedAffordances()
  }, STAGED_DRYRUN_DEBOUNCE_MS)
}

/**
 * Applies a template's properties, content and relations to the form.
 *
 * `preserveUserInput` is set on the automatic application that follows the
 * async template fetch. Templates arrive after the form is interactive, so a
 * user who started writing before the response landed had their body replaced
 * by the template's — the entity-reference picker was the visible symptom,
 * since inserting a reference and saving stored the template text instead.
 *
 * An explicit template choice from the dropdown passes false: replacing the
 * content is the whole point of picking one.
 */
function applyTemplate(template: Template, preserveUserInput = false) {
  // Apply template properties
  for (const [key, value] of Object.entries(template.properties)) {
    formData.value[key] = value
  }
  // Apply template content
  if (!preserveUserInput || !content.value) content.value = template.content
  // Apply template relations
  for (const rel of template.relations) {
    if (!relations.value[rel.relation]) {
      relations.value[rel.relation] = []
    }
    if (!relations.value[rel.relation].includes(rel.target)) {
      relations.value[rel.relation].push(rel.target)
    }
  }
  originalData.value = JSON.stringify({
    formData: formData.value,
    relations: relations.value,
    content: content.value,
  })
}

/**
 * Applies a Duplicate's copied values (TKT-Z8K2FS).
 *
 * Runs through `applyTemplate` so typed values, content and relations take the
 * one existing apply path, then marks every copied property user-touched — see
 * the `embeddedPrefill` prop doc for why that is load-bearing rather than
 * incidental.
 *
 * `preserveUserInput: false` because a duplicate is an explicit request for
 * this entity's values: a template default that landed first must lose. Nothing
 * the user typed can be at risk yet, since this runs before the form is
 * interactive.
 */
function applyEmbeddedPrefill() {
  const prefill = props.embeddedPrefill
  if (!prefill) return
  applyTemplate(
    {
      name: '',
      properties: prefill.properties,
      content: prefill.content,
      relations: Object.entries(prefill.relations).flatMap(([relation, peers]) =>
        peers.map((peer) => ({ relation, target: peer.id }))
      ),
    },
    false
  )
  for (const prop of Object.keys(prefill.properties)) {
    userTouched.value.add(prop)
  }

  // Register each peer's TYPE, the same obligation a pre-link carries
  // (see the `link_as: to` path above). `pickerTypes` is otherwise populated
  // only by RelationPicker, so a relation the form does not render as a field
  // would have no type entry — and reshapeLegacyToModern returns null for an id
  // it cannot type, aborting the WHOLE create with "Some related entities have
  // unknown types" and advice ("reload the form") that cannot help.
  //
  // A duplicate is MORE exposed than a pre-link: it carries whatever the source
  // had, including relation types this create form never renders. The type is
  // carried on the peer rather than guessed from an id prefix, so unlike that
  // path this cannot miss.
  for (const [key, peers] of Object.entries(prefill.relations)) {
    const types = pickerTypes.value[key] ?? new Map<string, string>()
    for (const peer of peers) types.set(peer.id, peer.type)
    pickerTypes.value[key] = types
    // Exempt from the ownership filter (BUG-KQSOJ2), for the reason the comment
    // above gives: a duplicate deliberately carries relations this form does
    // not render. They are fully typed here, so they were written before the
    // filter existed — dropping them now would silently narrow what Duplicate
    // copies, which is a behaviour change rather than a fix.
    prefilledRelations.value.add(key)
  }

  routePrefilledCardRelations(prefill.relations)

  // Re-baseline AFTER every mutation. applyTemplate baselines mid-way, and
  // routePrefilledCardRelations then deletes the keys it took ownership of —
  // so without this the form reads dirty the moment it opens and the discard
  // guard stops distinguishing anything.
  originalData.value = JSON.stringify({
    formData: formData.value,
    relations: relations.value,
    content: content.value,
  })
}

/**
 * Applies the card-delivery routing decided by [planPrefillRouting].
 *
 * The decision itself is a pure function in `prefillRouting.ts` — inverse keys
 * and symmetric self-inverse relations are where the subtlety is, and keeping
 * them out of this component is what makes them testable without a form mount.
 */
function routePrefilledCardRelations(prefilled: Record<string, { id: string; type: string }[]>) {
  // allFields, not fields: the visible set is filtered by affordances that
  // arrive with the dry-run, which has not run when the prefill lands. Whether
  // an edge is card-DELIVERED is a property of the form config, not of what is
  // currently on screen.
  const cardRelations = new Set(
    allFields.value.filter((f) => f.relation && f.widget === 'cards').map((f) => f.relation!)
  )
  if (cardRelations.size === 0) return

  const inverseByRelation = new Map<string, string>()
  for (const f of allFields.value) {
    if (!f.relation) continue
    const inverse = schemaStore.getInverseName(f.relation)
    if (inverse) inverseByRelation.set(f.relation, inverse)
  }

  const { cardRoutes } = planPrefillRouting(prefilled, cardRelations, inverseByRelation)
  for (const route of cardRoutes) {
    const state = pendingCardChanges.value.get(route.mapKey) ?? {
      entries: [],
      added: [],
      removed: [],
      updated: [],
    }
    for (const peer of route.peers) {
      // `entries` is what buildRelationsPatch actually emits; `added` only
      // decides whether the key is emitted at all. Populating one without the
      // other sends an EMPTY edge list, which reads as "unlink everything"
      // rather than as the copy's edges.
      if (!state.entries.some((e) => e.id === peer.id)) {
        state.entries.push({ id: peer.id, type: peer.type })
      }
      if (!state.added.some((a) => a.targetId === peer.id)) {
        state.added.push({ targetId: peer.id })
      }
    }
    pendingCardChanges.value.set(route.mapKey, state)
    // Drop the copy the card path now owns, so one edge cannot be emitted
    // twice under two different body keys.
    delete relations.value[route.sourceKey]
  }
}

function selectTemplate(name: string) {
  selectedTemplate.value = name
  const template = templates.value.find((t) => t.name === name)
  if (template) {
    // Reset to defaults first
    formData.value = {}
    relations.value = {}
    content.value = ''
    initializeDefaults()
    applyTemplate(template)
  }
}

function getTemplateLabel(name: string): string {
  if (name === '') return 'Default'
  // Capitalize first letter
  return name.charAt(0).toUpperCase() + name.slice(1)
}

// TKT-7YHKD1 "Create & add another": return the create-mode form to a blank
// state IN PLACE, without navigating and without remounting the component.
//
// The reset is CLEAN. A field or relation carries its value into the next
// record only if the operator marked it `keep_on_add_another` in
// data-entry.yaml. That default is the safe direction: a wrongly-cleared field
// costs one re-entry, while a wrongly-kept one silently writes a stale value
// into every subsequent record.
//
// Create-mode state lives in more places than the three obvious refs, and
// anything missed here leaks record N into record N+1 — see the enumeration
// below. Callers: handleSubmit('again') only. `onMounted` deliberately does
// NOT route through this (it has no prior state to clear and its own ordering
// around loadTemplates is different).
async function resetCreateForm() {
  if (isEdit.value || !formConfig.value) return

  // Capture the carry-over values BEFORE clearing. Read `allFields` rather
  // than the affordance-filtered `fields`: the carry-over set must not depend
  // on incidental affordance state (same reasoning as RR-00VT for defaults).
  const keptProps: Record<string, unknown> = {}
  const keptRels: Record<string, string[]> = {}
  // The id -> type map for the kept relations travels WITH them. `pickerTypes`
  // is cleared below, and `reshapeLegacyToModern` returns null if it cannot
  // resolve a type for every picker target — which makes handleSubmit abort the
  // whole save with "unknown types, reload the form", while the widget still
  // shows the kept peer. That is a silent lost record, so the reset does not
  // leave it to chance.
  //
  // Belt-and-braces in practice: `saveGeneration++` remounts the picker, and
  // RelationPicker's onMounted re-emits `update:types` for any pre-existing
  // outgoing selection precisely to cover this, so the map refills itself
  // (verified — removing this block keeps the e2e green). Kept anyway: it makes
  // the reset self-sufficient rather than dependent on a child's mount timing,
  // and the failure mode if that dependency ever breaks is a lost record with a
  // misleading "reload the form" message.
  const keptPickerTypes: Record<string, Map<string, string>> = {}
  for (const f of allFields.value) {
    if (!f.keep_on_add_another) continue
    if (f.property && formData.value[f.property] !== undefined) {
      keptProps[f.property] = formData.value[f.property]
    }
    if (f.relation && relations.value[f.relation]) {
      keptRels[f.relation] = [...relations.value[f.relation]]
      const types = pickerTypes.value[f.relation]
      if (types) keptPickerTypes[f.relation] = new Map(types)
    }
  }

  // Values. Content is never kept: a body is per-record by nature and there is
  // no field-level key to mark it.
  formData.value = {}
  relations.value = {}
  content.value = ''
  errors.value = {}
  userTouched.value = new Set()
  pickerTypes.value = {}
  stagedFiles.value = {}
  // An INCOMING RelationPicker's selection is latched here, not in `relations`
  // (updateIncomingPicker writes it via `incoming-changed`), so clearing
  // `relations` and remounting the widget does not reach it. Leaving it would
  // write record N's incoming relation onto record N+1 with the user having
  // touched nothing. RelationCards genuinely never render in create mode (they
  // need an entityId), but the picker does — the two share this map.
  pendingCardChanges.value.clear()

  // Staged-affordance verdicts describe the PREVIOUS record. Leaving them
  // would render record N+1 against record N's dry-run — and the dry-run fails
  // OPEN, so on failure the stale verdicts would persist with no signal.
  // Reset to the pre-dry-run state and re-derive below.
  fieldAffordances.value = {}
  relationAffordances.value = {}
  stagedVisibleProps.value = new Set()
  stagedAffordancesReady.value = false

  // Retained hidden-branch values belong to the form state we are replacing —
  // the same reason loadEntity calls this.
  //
  // Belt-and-braces TODAY: the retention map is only ever populated in EDIT
  // mode (useChangePolicy is gated on `enabled: () => isEdit.value` and
  // returns early at its entry point, because "create has no stored value to
  // lose"), so in create mode there is nothing here to release and no test can
  // observe its absence. Kept anyway because the cost is one call and the
  // failure it would guard is silent: if retention ever becomes enabled for
  // create, a value retained while hidden on record N would be restored over a
  // cleared field on N+1. Delete this only together with that gate.
  hiddenPolicy.releaseAll()

  // A clear-confirm dialog opened against the previous record must not apply
  // to this one. `formGeneration` is the existing staleness token for exactly
  // this ("bumped whenever the form's underlying entity state is replaced
  // wholesale").
  formGeneration.value++

  // Remount the relation widgets. This is REQUIRED, not cosmetic: an
  // incoming-direction RelationPicker keeps its selection in its own
  // `incomingValue` ref and ignores the `value` prop entirely, so clearing
  // `relations` above does not reach it — its record-N peers would re-emit as
  // additions and be written to record N+1 as duplicate links.
  saveGeneration.value++

  // Release the one-shot create latch only now that the form is blank again.
  createdEntityId.value = null

  idControls.reset()
  initializeDefaults()

  // Re-apply the template the user actually has selected, from the templates
  // already loaded at mount. NOT loadTemplates(): that refetches over HTTP per
  // record and force-selects templates[0], discarding the user's pill choice.
  const chosen = templates.value.find((t) => t.name === selectedTemplate.value)
  if (chosen) applyTemplate(chosen)

  // Kept values are re-applied AFTER defaults and the template, both of which
  // write formData — otherwise either would clobber the carried-over value.
  for (const [prop, value] of Object.entries(keptProps)) {
    formData.value[prop] = value
    // A kept value is one the user entered, so it must stay "touched" for the
    // commit filter (visibleWritablePropertiesForCommit) to include it.
    userTouched.value.add(prop)
  }
  for (const [rel, targets] of Object.entries(keptRels)) {
    relations.value[rel] = [...targets]
  }
  for (const [rel, types] of Object.entries(keptPickerTypes)) {
    pickerTypes.value[rel] = types
  }

  // Back to step 1. Ordering matters: `dirty` is already false by this point
  // (handleSubmit clears it before calling us), so the `router.replace` this
  // performs cannot trip the unsaved-changes leave guard.
  wizard.goTo(0)

  await refreshStagedAffordances()
  if (stagedUnmounted) return

  // Re-baseline dirty tracking LAST. adoptLockedFieldValues (inside the
  // dry-run above) mutates formData in place without touching originalData, so
  // re-seeding any earlier would leave a form with an entry-locked field dirty
  // the instant the dry-run resolves — a spurious "unsaved changes" prompt
  // after every single record.
  originalData.value = JSON.stringify({
    formData: formData.value,
    relations: relations.value,
    content: content.value,
  })
  dirty.value = false
}

// Validate the form. `scopeFields` restricts validation to a subset (used for
// per-step validation on wizard "Next"); when omitted, all shown fields are
// validated (single-page submit, or wizard final submit over visible steps).
// `requiredProps` are property keys made required by a matching `required_when`
// condition, in addition to the metamodel's own `required` flag.
//
// Errors are updated **only for the scope's fields**: existing errors on fields
// OUTSIDE the scope are preserved. This is what lets a wizard's per-step Next
// re-check just the current step while leaving flags on other errored steps
// standing until they're actually resolved. Returns whether the SCOPE is valid.
function validate(scopeFields?: FormFieldOrRelation[], requiredProps?: Set<string>): boolean {
  if (!entityType.value) return true

  const scope = scopeFields ?? fields.value
  // Only validate properties that are shown in the form (not hidden)
  const formPropertyNames = new Set(
    scope
      .filter((f): f is typeof f & { property: string } => !!f.property && !f.hidden)
      .map((f) => f.property)
  )

  // Start from the existing errors, then clear this scope's entries so a
  // fixed field drops out while out-of-scope errors are untouched.
  const next: Record<string, string> = { ...errors.value }
  for (const prop of formPropertyNames) delete next[prop]
  let scopeValid = true

  for (const [propName, propDef] of Object.entries(entityType.value.properties)) {
    // Skip properties not in the form - backend will validate them
    if (!formPropertyNames.has(propName)) continue

    const value = formData.value[propName]

    // Required check (metamodel `required` OR a matching `required_when`).
    //
    // A staged file counts as a value (BUG-L1DHC5). Staged files deliberately
    // live outside `formData` — a File there would be POSTed as the property's
    // value, and the server stamps that property itself once the bytes land —
    // so without this term a `required` file property can never be satisfied
    // and Create is blocked forever with no visible error.
    const isRequired = propDef.required || (requiredProps?.has(propName) ?? false)
    const hasStagedFile = (stagedFiles.value[propName]?.length ?? 0) > 0
    if (isRequired && !hasStagedFile && (value === undefined || value === null || value === '')) {
      next[propName] = 'This field is required'
      scopeValid = false
      continue
    }

    // Type-specific validation
    if (value !== undefined && value !== null && value !== '') {
      if (propDef.type === 'integer' && typeof value === 'string') {
        const num = parseInt(value, 10)
        if (isNaN(num)) {
          next[propName] = 'Must be a valid number'
          scopeValid = false
        }
      }

      if (propDef.type === 'date' && typeof value === 'string') {
        const date = new Date(value)
        if (isNaN(date.getTime())) {
          next[propName] = 'Must be a valid date'
          scopeValid = false
        }
      }

      if (propDef.values?.length) {
        const allowed = propDef.values
        const items = propDef.list && Array.isArray(value) ? value : [value]
        const invalid = items.some((v) => !allowed.includes(String(v)))
        if (invalid) {
          next[propName] = `Must be one of: ${allowed.join(', ')}`
          scopeValid = false
        }
      }
    }
  }

  errors.value = next
  return scopeValid
}

// The single affordance filter for BOTH render paths — flat `fields` and the
// wizard's `visibleStepFields`. Decides whether a config-declared field is
// rendered at all (readonly/options are separate, see isFieldReadonly).
//
// Edit mode (DEC-T0XIWQ): a configured field renders unless the server
// positively names it in `_redacted`. It previously rendered only if the key
// was already in `properties`, which conflated "redacted" with "never set" and
// made every unset property permanently unfillable (BUG-MLT9DE) — the field
// could not be filled in because it did not render, and did not render because
// it had never been filled in.
//
// Create mode is unchanged: the staged dry-run's candidate carries metamodel
// defaults, so `stagedVisibleProps` is a genuine visible-set (it distinguishes
// hidden from unset by construction) rather than an inference from absence.
//
// F19 flicker prevention: render unfiltered until affordances are available —
// during initial `loading` (edit) and until the first dry-run resolves
// (create). Otherwise a policy-hidden field would flash in then disappear. If a
// create-mode dry-run never resolves (fail-open, RR-HUQ3) the form stays
// unfiltered and usable; the commit gate is the real boundary.
function affordanceVisible(f: FormFieldOrRelation): boolean {
  if (!f.property) return true // relations / non-property fields untouched
  if (loading.value) return true
  if (isEdit.value) return !isPropertyRedacted(f.property, redactedProps.value)
  if (!stagedAffordancesReady.value) return true
  if (f.property in fieldAffordances.value) return true
  return stagedVisibleProps.value.has(f.property)
}

// A wizard step's fields to render: per-field `visible_when` (wizard layer)
// intersected with the affordance filter (same rule as flat `fields`).
function visibleStepFields(step: import('@/types').FormStep): FormFieldOrRelation[] {
  return wizard.visibleFieldsOf(step).filter(affordanceVisible)
}

// Fields in scope for submit: the visible fields of the currently-visible
// steps (for a flat/one-step form that's just its fields). Applies the same
// affordance filter as rendering (visibleStepFields), so validation never
// demands a policy-hidden field the user can't see or fill.
function submitScopeFields(): FormFieldOrRelation[] {
  return wizard.visibleSteps.value.flatMap((s) => visibleStepFields(s))
}

// Drop property keys under a condition-hidden step/field so a revealed-then-
// hidden branch is not persisted. Applied by BOTH submit paths — including on
// top of the create path's affordance prune — so the two pruning systems
// reconcile in one place. For a flat form with no conditions this is a no-op
// (nothing is hidden).
function pruneWizardHidden(props: Record<string, unknown>): Record<string, unknown> {
  const active = wizard.activeProperties.value
  const managed = wizard.managedProperties.value
  // Drop a key only if the wizard governs it (named by some step) AND it is not
  // currently active (its step/field is condition-hidden). A key no step
  // mentions — e.g. a metamodel default seeded into form state — is left as-is,
  // matching how a single-page form submits it.
  return Object.fromEntries(
    Object.entries(props).filter(([key]) => !managed.has(key) || active.has(key))
  )
}

// Relation analogue of pruneWizardHidden: drop a relation's edges when it is
// wizard-governed but currently hidden (its step/field visible_when is false),
// so a revealed-then-hidden relation is not persisted. `rels` is keyed by
// relation name (as relations.value is).
function pruneWizardHiddenRelations(rels: Record<string, string[]>): Record<string, string[]> {
  const active = wizard.activeRelations.value
  const managed = wizard.managedRelations.value
  return Object.fromEntries(
    Object.entries(rels).filter(([rel]) => !managed.has(rel) || active.has(rel))
  )
}

/**
 * Relation keys a prefill channel seeded, which the ownership filter must keep
 * even when no field on this form names them (BUG-KQSOJ2).
 *
 * There are TWO independent channels and both must be covered:
 * `link_relation`/`link_peer` (the create button's pre-link) and `rel.<name>`
 * query params. Neither is constrained to a relation the form renders.
 *
 * Carrying an unowned prefill is deliberate. Its edge is the caller's explicit
 * instruction, so dropping it would write nothing and say nothing — the exact
 * "create succeeds, section still empty, looks like a stale page" failure the
 * post-condition check below exists to catch. Kept here, an untypeable prefill
 * still fails loudly instead.
 */
function prefilledRelationKeys(): Set<string> | undefined {
  const keys = new Set(prefilledRelations.value)
  if (linkParams.value?.as === 'to') keys.add(linkParams.value.relation)
  return keys.size > 0 ? keys : undefined
}

// Error text for a relations payload that cannot be typed.
//
// Names the offending relation rather than saying only "some related entities":
// the operator reading this is the one who can fix it, and the old wording sent
// them to a reload that could not help. `lead` distinguishes the two call sites
// ("Save aborted" vs autosave's "Relation changes were not saved") without
// duplicating the rest of the sentence.
//
// The remedy differs by cause, so the message must too. A relation this form
// RENDERS normally gets its types from the candidate load, so a reload is
// genuine advice. A relation carried only as a prefill (`rel.` param or a
// create button's pre-link) has no field to load anything, so a reload changes
// nothing — that one is a config problem, and saying "reload" would be
// confidently wrong.
function relationTypeErrorMessage(
  filteredRelations: Record<string, string[]>,
  lead: string
): string {
  const bad = untypedRelationKeys(filteredRelations, pickerTypes.value)
  const which = bad.length > 0 ? ` for "${bad.join('", "')}"` : ''
  const owned = ownedRelationKeys(allFields.value)
  const anyRendered = bad.length === 0 || bad.some((rel) => owned.has(rel))
  const remedy = anyRendered
    ? `Reload the form and try again; if it persists, ask your operator to check ` +
      `this form's relation fields.`
    : `This form has no field for it, so it arrived as a pre-filled link. Ask ` +
      `your operator to check the originating create button's section config ` +
      `against this form.`
  return `${lead}: could not resolve the entity type of every related item${which}. ${remedy}`
}

// Property keys made required right now by a matching `required_when`.
function requiredWhenProps(scope: FormFieldOrRelation[]): Set<string> {
  const req = new Set<string>()
  for (const f of scope) {
    if (f.property && wizard.isFieldRequired(f)) req.add(f.property)
  }
  return req
}

// Wizard "Next": validate only the current step's visible fields; advance only
// when valid, so an invalid step blocks progression with per-field errors.
// `validate` is scope-local, so it re-checks just this step and leaves any
// flags on OTHER errored steps standing until they're resolved.
function handleNext() {
  const step = wizard.currentStepDef.value
  if (!step) return
  // Same scope as the step renders (visibleStepFields), so Next doesn't block
  // on a policy-hidden field the user can't see.
  const scope = visibleStepFields(step)
  if (!validate(scope, requiredWhenProps(scope))) return
  wizard.next()
}

function handleBack() {
  // Back never validates and never clears errors — navigating isn't fixing.
  wizard.back()
}

// Clicking a step pill jumps straight there. A deliberate jump is not gated by
// per-step validation (unlike Next): visible_when keeps unmet branches hidden
// and the final Submit re-validates every visible step, so a jump can't persist
// invalid data. Errors are left intact — a flag clears when its field is fixed,
// not when the user navigates away.
function handleStepClick(index: number) {
  if (index === wizard.currentStep.value) return
  wizard.goTo(index)
}

// On a failed submit, take the user to the first step that has an error and
// focus its first invalid field, so the fix is one glance + zero hunting. For a
// one-step form this just focuses the field on the only step.
function focusFirstError() {
  // First errored property in visible order.
  let firstStep = Infinity
  let firstProp: string | null = null
  for (const property of Object.keys(errors.value)) {
    const idx = wizard.visibleStepIndexForProperty(property)
    if (idx >= 0 && idx < firstStep) {
      firstStep = idx
      firstProp = property
    }
  }
  if (firstProp === null) return
  wizard.goTo(firstStep)
  // Focus after the step renders.
  const prop = firstProp
  nextTick(() => {
    const el = document.getElementById(`field-${prop}`)
    el?.scrollIntoView({ block: 'center', behavior: 'smooth' })
    el?.focus()
  })
}

/**
 * The body editor, so a save can flush its pending change.
 *
 * The editor's markdown listener is debounced; without the flush, a change
 * made within that window is not in `content` yet and the save stores the
 * previous body. Inserting an entity reference and immediately submitting is
 * the case that exposed it.
 */
const markdownEditorRef = ref<{ flush?: () => void } | null>(null)

/** Pushes any pending editor change into `content` before it is read. */
function flushEditor() {
  markdownEditorRef.value?.flush?.()
}

// `mode` selects the terminal outcome of a successful create, and NOTHING
// else: validation, payload assembly, the create call, the auto-link and the
// staged-attachment upload are shared verbatim so the two buttons cannot build
// different payloads.
//
//   'navigate' — the original behaviour: toast, then router.push to the new
//                entity (or return_to).
//   'again'    — TKT-7YHKD1: toast naming the id, then reset the form in place
//                for the next record. No navigation.
//
// MUST stay defaulted: handleKeydown (Cmd+Enter) and defineExpose's `submit`
// both call this bare, and the inline-create host depends on the 'navigate'
// contract.
type SubmitMode = 'navigate' | 'again'

async function handleSubmit(mode: SubmitMode = 'navigate') {
  flushEditor()
  await nextTick()
  if (!formConfig.value) return
  // RR-HJLLUF: re-entrancy guard. PendingButton suppresses its own repeat
  // clicks, but handleKeydown calls handleSubmit() directly, so Cmd+Enter
  // twice would run the whole create path twice — two entities, and (since
  // TKT-7K3BJF) two sets of uploads. The guard belongs on the operation, not
  // on one of its callers, or the next caller reintroduces the hole. The
  // window is now the create POST *plus* N sequential uploads, so this is
  // seconds wide on a large file, not milliseconds.
  if (saving.value) return
  // Already created once from this form instance (RR-4QO887): a second submit
  // would create a duplicate, not retry the failed attachment upload.
  if (createdEntityId.value) return
  // Edit mode has no explicit submit — autosave persists per field. Guard
  // against an Enter-key form submit doing anything (there's no Save button).
  //
  // This early return is load-bearing beyond the Enter-key case: because edit
  // mode never bulk-submits, an untouched field can never reach a payload, so
  // a redacted field's withheld value cannot be overwritten with empty
  // (DEC-T0XIWQ). Everything below is therefore the CREATE path only. If you
  // ever add a bulk edit submit here, you must first prune properties the
  // server reported in `_redacted` that the user did not touch.
  if (isEdit.value) return
  const scope = submitScopeFields()
  if (!validate(scope, requiredWhenProps(scope))) {
    focusFirstError()
    return
  }

  saving.value = true
  try {
    // Card-managed relations are not put into the legacy
    // `filteredRelations` IDs-only map — they're delivered through
    // pendingCardChanges and the unified PATCH-with-relations shape.
    // `allFields`, not `fields`: whether an edge is card-DELIVERED is a
    // property of the form config, not of what affordances currently show —
    // same reasoning as routePrefilledCardRelations. Reading the visible set
    // here would let an affordance-hidden card relation escape this exclusion
    // and reach the payload as a stale full-replace.
    const cardRelations = new Set(
      allFields.value.filter((f) => f.relation && f.widget === 'cards').map((f) => f.relation!)
    )
    const inverseByRelation = new Map<string, string>()
    for (const f of allFields.value) {
      if (!f.relation) continue
      const inverse = schemaStore.getInverseName(f.relation)
      if (inverse) inverseByRelation.set(f.relation, inverse)
    }
    // Drop relations this form does not own (BUG-KQSOJ2) — `relations.value` is
    // seeded from the entity GET, which carries every relation the entity has,
    // not just the rendered ones. `allFields`, not `fields`: ownership is a
    // property of the config, so an affordance-hidden relation still belongs to
    // this form.
    const owned = ownedRelationKeys(allFields.value)
    const prefilled = prefilledRelationKeys()
    // Then drop relations under a condition-hidden branch (mirrors property
    // pruning), then card-managed ones (delivered via pendingCardChanges).
    const prunedRelations = pruneWizardHiddenRelations(
      filterOwnedRelations(relations.value, owned, prefilled)
    )
    const filteredRelations: Record<string, string[]> = {}
    for (const [rel, ids] of Object.entries(prunedRelations)) {
      if (!cardRelations.has(rel)) {
        filteredRelations[rel] = ids
      }
    }

    // Build the modern relations body. Picker selections (IDs-only
    // in memory) are reshaped to JSON:API §9 wrappers via pickerTypes;
    // card edits already carry per-edge meta. Incoming-suffix entries
    // become inverse-named body keys via the inverseByRelation lookup
    // (TKT-GFQK).
    const modernRelations = buildRelationsPatch(pendingCardChanges.value, inverseByRelation)
    const reshapedPickers = reshapeLegacyToModern(filteredRelations, pickerTypes.value)
    if (!reshapedPickers) {
      // A target with no resolved type. Ownership filtering above removed the
      // common cause (an unrendered relation from the entity GET), leaving two
      // reachable cases, which relationTypeErrorMessage tells apart: a field
      // this form renders failed to type one of its own targets (a reload may
      // help), or a prefilled relation with no field here carried a peer whose
      // type nothing could resolve (a config problem — a reload will not help).
      uiStore.error(relationTypeErrorMessage(filteredRelations, 'Save aborted'))
      // Drop the outgoing card-edit Map entries so they aren't
      // mistakenly cleared on success below.
      for (const key of Array.from(pendingCardChanges.value.keys())) {
        if (key.endsWith(OUTGOING_SUFFIX)) pendingCardChanges.value.delete(key)
      }
      saving.value = false
      return
    }
    const relationsPayload: ModernRelationsField = { ...reshapedPickers, ...modernRelations }

    // POST-CONDITION: a `link_as: to` pre-link must actually be in the payload.
    //
    // `relations.value` is NOT the payload. Two filters sit between them —
    // `pruneWizardHiddenRelations` (drops a relation on an inactive wizard step)
    // and the `cardRelations` exclusion (card-managed edges are supposed to
    // arrive via `pendingCardChanges`, which the prefill does not write) — and
    // either one silently eats a prefilled edge. The create then succeeds with
    // no relation and no error: the user returns to the originating entity and
    // the section is still empty, which looks exactly like a stale page.
    //
    // Checked rather than prevented because the right answer depends on the
    // form: routing the prefill into `pendingCardChanges` would duplicate the
    // card widget's own bookkeeping, and forcing a wizard step active would
    // override an author's `visible_when`. Failing loudly puts the problem in
    // front of the operator who configured it, which is the only person who can
    // fix it.
    if (linkParams.value?.as === 'to') {
      const rel = linkParams.value.relation
      const carried = (relationsPayload[rel]?.data ?? []).some(
        (r) => r.id === linkParams.value!.peer
      )
      if (!carried) {
        uiStore.error(
          `Cannot pre-link this ${formConfig.value.entity} to ${linkParams.value.peer}: ` +
            `the form's "${rel}" field cannot carry it. Ask your operator to check the ` +
            `create button's section config against this form.`
        )
        saving.value = false
        return
      }
    }

    const payload: {
      id?: string
      prefix?: string
      world?: string
      properties: Record<string, unknown>
      relations: ModernRelationsField
      content?: string
    } = {
      // For a wizard, drop values under a hidden step/field so a toggled-off
      // branch doesn't persist stale data (matches OpenVWR's isFieldEnabled).
      // Single-page forms submit formData as-is. The create path re-derives
      // this from the affordance-pruned set below (line ~734); this covers the
      // edit path.
      properties: pruneWizardHidden(formData.value),
      relations: relationsPayload,
      content: content.value || undefined,
    }

    // Create only — the isEdit early return above means this is never an
    // update. RelationCards never render in create mode (they require an
    // entityId), but an INCOMING RelationPicker does, and it routes through
    // `pendingCardChanges` too (updateIncomingPicker) — so that map is NOT
    // necessarily empty here, and relationsPayload is composed from both it
    // and the reshaped outgoing picker selections.
    //
    // TKT-3I5U: send only visible + writable property keys; the server
    // fills hidden / read-only defaults after the affordance gate. For a
    // wizard, also drop keys under a condition-hidden step/field — a
    // `visible_when=false` branch wins over the affordance filter's
    // userTouched-preserve rule, so a revealed-then-hidden field is not
    // persisted (TKT-CHLAJ).
    payload.properties = pruneWizardHidden(visibleWritablePropertiesForCommit())
    // The world the create button opened this form in decides which face the
    // new entity starts in — the server maps it through `worlds.<name>.create`.
    // A faced type has no default row to fall back to, so omitting this is a
    // refusal rather than a silent default (BUG-HC6I2T).
    //
    // An embedded form takes the world from its HOST, explicitly. `useWorld`
    // reads the route, and an embedded form's route is the host page's — so
    // this happens to be the same value. Taking it as a prop makes that a
    // stated contract rather than a coincidence that a later refactor of
    // `embedded` could silently break, the way the empty-query rule would
    // otherwise suggest the world is dropped too.
    const createWorld = props.embedded ? props.embeddedWorld : worldParam.value
    if (createWorld) payload.world = createWorld
    Object.assign(payload, idControls.buildPayloadFields())
    const entity = await entitiesStore.create(formConfig.value.entity, payload)
    createdEntityId.value = entity.id
    // DEC-HWZHA soft conditions ride the 200 create response; surface them or
    // they are invisible. (Edit-mode warnings come through autosave's own
    // response handling — this is the create channel.)
    surfaceWarnings(entity.warnings)

    // Auto-linking from link_* params (a section or side-panel create button).
    //
    // `link_as` names the role of the NEW entity, which is the server's
    // definition (internal/dataentry/sections.go) and the only one that lets a
    // caller express both directions:
    //
    //   link_as=to    new entity is the relation's TO. The create payload
    //                 already carries `relation: [peer]`, which the server
    //                 writes as new --relation--> peer. Nothing to do here.
    //   link_as=from  new entity is the relation's FROM, so the edge runs
    //                 new --relation--> peer in the OTHER direction from what
    //                 the payload can express. Needs a second call.
    //
    // This comment previously read the flag the opposite way ("for link_as=from
    // we need peer --relation--> new_entity"), which was untestable while the
    // side panel emitted param names the form never read (TKT-R4BMJM). Once the
    // names were fixed, an incoming section produced a REDUNDANT reverse call
    // whose peer-type lookup then failed on a prefix-less id like `backend` —
    // reporting a link failure for an edge the payload had already written.
    if (linkParams.value && linkParams.value.as === 'from') {
      try {
        const { relation, peer } = linkParams.value
        // new --relation--> peer, addressed from the NEW entity.
        //
        // The endpoint is `/{plural}/{from}/relations/{rel}` with the TO in the
        // body, so the source entity is the one in the path. Addressing it from
        // the peer instead would write the edge backwards — and would also need
        // the peer's TYPE, which is only derivable from its id prefix. That
        // lookup fails for a legitimately prefix-less id (the demo project's
        // category ids are `backend`, `devops`), so the old form of this call
        // could not link to one at all. The new entity's type is known outright.
        await createRelation(entity.type, entity.id, relation, peer)
      } catch (linkErr) {
        console.warn('Auto-link failed:', linkErr)
        // Surfaced on EVERY path, not just 'again'. The older reasoning — that
        // the navigate path can stay quiet because the user lands on the entity
        // and can see the gap — does not hold here: `return_to` sends them back
        // to the ORIGINATING entity, where a missing link looks exactly like a
        // section that has not refreshed yet.
        uiStore.error(`${entity.id} was created, but linking it failed. Link it manually.`)
        // Continue with navigation even if link fails: the entity exists, and
        // stranding the user on a form that would re-create it on resubmit is
        // worse than landing them somewhere they can fix the link.
      }
    }

    // TKT-7K3BJF phase 2: upload staged attachments against the id the server
    // just minted. Ordering here is load-bearing (RR-6ZAOMK) — uploads must
    // run BEFORE the dirty reset, the embedded early-return, and the success
    // toast, so that a failure can keep the form dirty, still reach the
    // inline-create host, and avoid claiming success it didn't achieve.
    const uploadFailures = hasStagedFiles()
      ? await uploadStagedFiles(formConfig.value.entity, entity.id)
      : []
    // RR-4QO887: clear staged files and drop the dirty flag even when an
    // upload failed. Keeping them looks like it preserves a retry, but this
    // path navigates away (or, when embedded, the host closes the modal —
    // RR-7T94EF), unmounting the form and discarding the File objects
    // regardless. Worse, a still-dirty form whose submit re-runs the WHOLE
    // create path turns "retry" into a second entity. Recovery is on the
    // created entity, whose file widget uploads directly; the error message
    // names exactly which files to re-attach.
    stagedFiles.value = {}
    dirty.value = false

    // Embedded: hand the entity to the host and stop. No navigation (it would
    // unmount the form that opened us, taking its draft with it) and no toast
    // — the host reports the creation itself, next to the link step the user
    // still has to complete.
    if (props.embedded) {
      if (uploadFailures.length > 0) uiStore.error(stagedFailureMessage(uploadFailures))
      emit('inline-created', entity)
      return
    }

    if (uploadFailures.length > 0) {
      // The entity exists — say so, and name exactly which files did not make
      // it. A bare success toast followed by an error would describe one user
      // action twice, contradicting itself.
      uiStore.error(stagedFailureMessage(uploadFailures))
    } else if (mode === 'again') {
      // Name the id: the user is NOT taken to the entity, so this toast is the
      // only evidence it exists. (Toast carries no link — the store's Toast
      // type has no action field; making it clickable would change every toast
      // in the app and is deliberately out of scope.)
      uiStore.success(`Created ${entity.id}. Form cleared for the next one.`)
      announce(`Created ${entity.id}. Form cleared for the next entry.`)
    } else {
      uiStore.success('Entity created successfully')
    }

    // RR-QQQ2JR: the upload phase can outlive the component (the user clicks
    // away mid-upload), so re-check before touching the router — every other
    // async path here already guards on this flag (RR-2PZB). A push on a torn-
    // down component is a no-op today; this keeps the discipline uniform.
    if (stagedUnmounted) return

    // TKT-7YHKD1: stay put and blank the form for the next record.
    //
    // AWAITED, and inside the try, on purpose: the reset is async, and
    // `saving` is only cleared by the finally below. Awaiting here keeps
    // `saving` true across the whole reset, so the window in which
    // `createdEntityId` has been released but the form is not yet blank is
    // closed. That matters because handleKeydown calls handleSubmit()
    // directly — Cmd+Enter bypasses PendingButton's repeat-click suppression
    // entirely.
    if (mode === 'again') {
      await resetCreateForm()
      return
    }

    // Navigate to return_to or entity detail.
    //
    // The world rides along to the redirect as well as into the create body:
    // it is what chose the new entity's face, so it is also the world that
    // face is visible in. Landing without it showed "not in this world" for
    // something just created. The list's `create_world` says which world that
    // is; useWorld picked it off the `?world=` the create button carried.
    if (returnTo.value) {
      router.push(returnTo.value)
    } else {
      router.push({
        path: `/entity/${formConfig.value.entity}/${entity.id}`,
        query: worldParam.value ? { world: worldParam.value } : {},
      })
    }
  } catch (err) {
    // Suppress cancellation errors from rapid navigation in Firefox
    // (see BUG-6C3V). A save that was interrupted by navigation is
    // not a user-facing failure; the user clicked away before the
    // save completed, which is their choice.
    if (isCancelledFetch(err)) return
    const validationErrors = err instanceof ApiError ? err.validationErrors : []
    if (validationErrors.length > 0) {
      const next = { ...errors.value }
      for (const e of validationErrors) {
        if (e.field) {
          next[e.field] = e.message || e.detail || 'Invalid value'
        }
      }
      errors.value = next
      uiStore.error('Please fix the validation errors')
    } else {
      uiStore.error(getErrorMessage(err, 'Failed to save entity'))
    }
    console.error(err)
  } finally {
    saving.value = false
  }
}

/**
 * Whether this SPA instance has somewhere in-app to go back to.
 *
 * `history.state.back` is written by vue-router on a push and is the only
 * reliable signal here: a full page load (direct URL, bookmark, refresh)
 * leaves it null no matter how deep the browser's history already is.
 * `window.history.length` cannot be used for this — it counts entries from
 * before the app was ever loaded, so it reads >= 2 even on a fresh open
 * (BUG-ZE4354).
 */
function hasAppHistory(): boolean {
  return (router.options.history.state as { back?: unknown } | null)?.back != null
}

/**
 * Cancel/Back must land somewhere inside the app.
 *
 * `router.back()` alone is a browser-history operation with no route target,
 * so on a directly-opened form there is no in-app entry behind it and back
 * walks out of the SPA entirely — which reads to the user as "the button does
 * nothing" (BUG-ZE4354). Resolution order mirrors `useBackTarget` and the
 * submit path, which already honours `returnTo`.
 */
function handleCancel() {
  if (props.embedded) {
    emit('inline-cancelled')
    return
  }
  if (returnTo.value) {
    router.push(returnTo.value)
    return
  }
  if (hasAppHistory()) {
    router.back()
    return
  }
  // Opened cold: fall back to the entity type's list, or the dashboard when
  // no list is configured for it.
  const listId = formConfig.value
    ? schemaStore.findListIdForEntityType(formConfig.value.entity)
    : undefined
  router.push(listId ? `/list/${listId}` : '/')
}

// TKT-7S5735 — the propose/commit seam.
//
// A widget edit arrives here as a PROPOSAL, not a write. The order is fixed and
// load-bearing:
//
//   1. work out what the change would hide, against hypothetical bindings
//   2. decide (policy)
//   3. only then mutate formData and arm the write
//
// Steps 1-2 happen before any mutation, which is what the old code could not
// do: `updateField` wrote `formData` on its first line, and since visibility is
// a pure function of `formData`, "what would hide?" was only answerable after
// the fact. Every BUG-FB0LN8 fix tried to reconstruct the prior state from that
// position and produced a near-miss.
//
// Today every policy still resolves synchronously, so `proposeChange` always
// returns 'applied' and no dialog can interleave. The seam exists so that an
// interactive policy (`clear_when_hidden: confirm`) can return 'rejected'
// without a single byte having changed — see useProposal.
// Bumped whenever the form's underlying entity state is replaced wholesale, so
// a dialog opened against the old state can be recognised as stale rather than
// applied to the new one.
const formGeneration = ref(0)

// True while a clear-confirm dialog is open. `useConfirm` is a SINGLETON that
// returns its in-flight promise to a concurrent caller, so a second `confirm()`
// would receive THIS dialog's answer — the navigation guard would silently
// inherit "yes, clear the field" as "yes, leave anyway". Guarding here is
// cheaper and clearer than making the dialog re-entrant.
const confirmingClear = ref(false)

// Bumped to force the field widgets to re-read their values from `formData`.
// Needed only on a DECLINED proposal: the widget already moved its own DOM,
// and an unchanged bound value gives Vue nothing to patch back.
const fieldsRenderKey = ref(0)

const changePolicy = useChangePolicy({
  bindings: conditionBindings,
  activeNow: () => wizard.activeProperties.value,
  activeFor: (b) => wizard.activePropertiesFor(b),
  managed: () => wizard.managedProperties.value,
  valueOf: (p) => formData.value[p],
  retain: (p, v) => hiddenPolicy.retain(p, v),
  apply: (proposal) => updateField(proposal.property, proposal.value),
  onHidden: applyHidePolicy,
  // Create has no stored value to lose — RR-O4SRG's drop-on-commit owns it.
  enabled: () => isEdit.value,
  policyFor: (p) => clearWhenHiddenOf(fieldByProperty.value.get(p)),
  isEmpty: (p) => isClearedForType(formData.value[p], entityType.value?.properties?.[p]),
  isUnreadable: (p) => isPropertyRedacted(p, redactedProps.value),
  generation: () => formGeneration.value,
  restore: () => {
    // `formData` still holds the previous value — the decline wrote nothing.
    // Only the widget's own DOM moved, and since the bound `model-value` is
    // unchanged, Vue has no reason to patch it back: the control would keep
    // displaying the value the user just declined.
    //
    // Bumping the render key remounts the field list, so every widget
    // re-reads its value from `formData`. Deliberately NOT done by writing a
    // sentinel through `formData` — that would fire the visibility watcher on
    // a value that was never real.
    fieldsRenderKey.value++
  },
  askToClear: async (properties) => {
    confirmingClear.value = true
    try {
      return await confirm({
        title: properties.length > 1 ? 'Clear these fields?' : 'Clear this field?',
        message: clearConfirmMessage(properties),
        confirmLabel: 'Clear',
        danger: true,
      })
    } finally {
      confirmingClear.value = false
    }
  },
})

/**
 * Name each field and show what it currently holds, so the user can weigh the
 * loss rather than guess at it. Values are already redaction-filtered — a
 * property the principal cannot see is absent from `formData` entirely.
 */
/** Render a value for human eyes: arrays as a list, objects as JSON. */
function describeValue(value: unknown): string {
  if (Array.isArray(value)) return value.map((v) => String(v)).join(', ')
  if (value && typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function clearConfirmMessage(properties: string[]): string {
  const lines = properties.map((p) => {
    const label = fieldByProperty.value.get(p)?.label || p
    return `• ${label}: ${describeValue(formData.value[p])}`
  })
  return [
    'This change hides the following, which will be cleared:',
    '',
    ...lines,
    '',
    'Continue?',
  ].join('\n')
}

/** Widget edits enter here. See useChangePolicy for why this is not a write. */
async function proposeChange(property: string, value: unknown): Promise<ProposalOutcome> {
  return changePolicy.propose(property, value, formData.value[property])
}

function updateField(property: string, value: unknown) {
  formData.value[property] = value
  // Clear a standing validation error for this field as the user edits it, so
  // the wizard's per-step error flags and summary update live. A full re-check
  // happens on the next Next/Submit; this just avoids a stale "needs attention"
  // marker after the user has addressed it.
  if (errors.value[property]) {
    const next = { ...errors.value }
    delete next[property]
    errors.value = next
  }
  checkDirty()
  // TKT-3I5U: in create mode, re-derive affordances from the staged
  // entity's new values (value-dependent verdicts) — debounced. Also
  // track that the user explicitly touched this key (RR-2U2D) so the
  // commit-side filter never drops it even if the dry-run hasn't
  // resolved yet for it.
  if (!isEdit.value) {
    userTouched.value.add(property)
    scheduleStagedAffordances()
    return
  }
  if (!autoSave.value) return
  // TKT-E6094: clear semantics per type. For string/list properties an
  // empty value means "user cleared" → properties_unset. Boolean false
  // is a legitimate value, never an unset.
  const def = entityType.value?.properties[property]
  if (isClearedForType(value, def)) {
    autoSave.value.scheduleUnset(property)
  } else {
    autoSave.value.scheduleFieldSave(property, value)
  }
}

function updateRelation(relation: string, value: string[]) {
  relations.value[relation] = value
  checkDirty()
  // Legacy IDs-only relation widget. Autosave routes this through the
  // pendingCardChanges map: any change triggers a relations PATCH.
  autoSave.value?.scheduleRelationsChange()
}

function updateRelationTypes(relation: string, types: Map<string, string>) {
  pickerTypes.value[relation] = types
}

// Pending relation card changes (for batch save)
const pendingCardChanges = ref<Map<string, RelationCardState>>(new Map())

// TKT-E6094: autosave is mounted only in edit mode. In create mode
// the user explicitly clicks Save; the form delays the entity into
// existence until then.
const autoSave = computed(() => {
  if (!isEdit.value || !props.entityId || !formConfig.value) return null
  return _autoSaveInstance.value
})
// Lazy holder so we construct the composable once per (entityId, formId).
const _autoSaveInstance = ref<ReturnType<typeof useAutoSave> | null>(null)

// Last server state seen by `loadEntity`, kept as the autosave merge base
// (TKT-DPBQ7S). SectionEditForm passes `initialServerSnapshot` and
// EntityDetail calls `recordServerSnapshot`; DynamicForm did NEITHER, so
// `lastSeenServer` stayed empty on the main edit form until the first PATCH
// response came back — leaving no baseline for no-op suppression or any
// future three-way merge.
//
// This MUST be a clone, not the entity object the form goes on to spread
// into `formData`/`content`: if the base aliased the form's own copy, every
// later keystroke would mutate the "server" side too and a three-way merge
// would degenerate into a two-way one (base === ours, always).
let _pendingServerSnapshot: Entity | null = null

function snapshotServerState(entity: Entity): Entity {
  const snap: Entity = {
    ...entity,
    properties: { ...(entity.properties ?? {}) },
    relations: entity.relations ? { ...entity.relations } : entity.relations,
  }
  // Freeze the properties bag so an accidental write to the baseline fails
  // loudly in dev instead of silently corrupting the merge base.
  Object.freeze(snap.properties)
  return Object.freeze(snap)
}

// Hand a fresh server snapshot to autosave. Before the composable exists
// (first load runs ahead of construction in onMounted) it is parked in
// `_pendingServerSnapshot` and passed as `initialServerSnapshot`.
function recordServerBaseline(entity: Entity) {
  const snap = snapshotServerState(entity)
  if (_autoSaveInstance.value) {
    _autoSaveInstance.value.recordServerSnapshot(snap)
    _pendingServerSnapshot = null
  } else {
    _pendingServerSnapshot = snap
  }
}
// Dirty-registry cleanup, assigned in onMounted (after awaits) and run
// from the top-level onBeforeUnmount.
let unregisterDirtyForm: (() => void) | null = null

function buildAutoSaveRelationsBody(): ModernRelationsField | null {
  // Mirror handleSubmit's body assembly. Two sources of relation
  // edits flow through autosave:
  //   - card-managed widgets (`pendingCardChanges`) — modern shape
  //     via buildRelationsPatch (per-edge meta + content).
  //   - legacy IDs-only widgets (`relations`) — non-card pickers
  //     write IDs; reshapeLegacyToModern wraps them in {data:[{type,id}]}
  //     so they ride the same modern PATCH.
  //
  // Returns null when neither source is dirty.
  const inverseByRelation = new Map<string, string>()
  const cardRelations = new Set<string>()
  if (formConfig.value) {
    // `allFields`, not `fields`: ownership is a property of the config, so an
    // affordance-hidden relation still belongs to this form and must not be
    // treated as foreign (and silently dropped) while it is hidden.
    for (const f of allFields.value) {
      if (!f.relation) continue
      const inv = schemaStore.getInverseName(f.relation)
      if (inv) inverseByRelation.set(f.relation, inv)
      if (f.widget === 'cards') cardRelations.add(f.relation)
    }
  }
  // Legacy picker edits — non-card relations this form owns. The ownership
  // filter (BUG-KQSOJ2) drops keys the entity GET carried but no outgoing field
  // renders; without it one untyped foreign key aborts the whole relations
  // PATCH, including the edit the user just made. Autosave is edit-mode only,
  // so there is no `link_as` prefill to exempt here.
  const owned = ownedRelationKeys(allFields.value)
  const filteredRelations: Record<string, string[]> = {}
  for (const [rel, ids] of Object.entries(filterOwnedRelations(relations.value, owned))) {
    if (cardRelations.has(rel)) continue
    filteredRelations[rel] = ids
  }
  const modernCards = buildRelationsPatch(pendingCardChanges.value, inverseByRelation)
  const hasModernCards = Object.keys(modernCards).length > 0
  const hasLegacy = Object.keys(filteredRelations).length > 0
  if (!hasModernCards && !hasLegacy) return null
  // Reshape legacy IDs to modern shape (autosave always uses modern;
  // shape_mixed 400 otherwise).
  const reshaped = hasLegacy ? reshapeLegacyToModern(filteredRelations, pickerTypes.value) : {}
  if (reshaped === null) {
    // A rendered picker target without a known type. Surface and skip —
    // autosave is best-effort. Ownership filtering above already removed the
    // unrendered-relation cause, so this is a genuine picker gap.
    uiStore.error(relationTypeErrorMessage(filteredRelations, 'Relation changes were not saved'))
    return null
  }
  return { ...reshaped, ...modernCards }
}

function updateRelationCards(relation: string, state: RelationCardState) {
  pendingCardChanges.value.set(relation, state)
  checkDirty()
  autoSave.value?.scheduleRelationsChange()
}

// Bridge incoming-direction RelationPicker changes into the pending-
// changes map under an `-incoming` suffix. After TKT-GFQK these flow
// through the SAME unified PATCH as outgoing — buildRelationsPatch
// remaps the suffix to the relation's inverse body key, and the
// backend's resolveDirection treats it as a "path entity is target"
// write. RelationPicker emits enough state (loadedEntries +
// currentEntries) for us to build a proper RelationCardState the
// builder can consume.
function updateIncomingPicker(relation: string, state: RelationPickerIncomingState) {
  pendingCardChanges.value.set(`${relation}${INCOMING_SUFFIX}`, {
    entries: state.currentEntries,
    added: state.added,
    removed: state.removed,
    updated: [],
  })
  checkDirty()
  autoSave.value?.scheduleRelationsChange()
}

// Surface soft validation warnings from a mutation response as a
// non-blocking toast. Per DEC-HWZHA, soft conditions (target type
// mismatch, unknown meta key, required-meta unset, etc.) ride on the
// 200 response rather than failing it. Without this, the conditions
// would be invisible to the user.
function surfaceWarnings(warnings: { code: string; path: string; detail: string }[] | undefined) {
  if (!warnings || warnings.length === 0) return
  const codes = [...new Set(warnings.map((w) => w.code))].join(', ')
  uiStore.warning(`Saved with ${warnings.length} warning(s): ${codes}`)
}

function updateContent(value: string) {
  content.value = value
  checkDirty()
  autoSave.value?.scheduleContentSave(value)
}

function checkDirty() {
  const currentData = JSON.stringify({
    formData: formData.value,
    relations: relations.value,
    content: content.value,
  })
  const hasCardChanges = pendingCardChanges.value.size > 0
  // RR-EUX4BX: staged files live outside formData by design, so they are
  // invisible to the serialized comparison above. Without this term, attaching
  // a file and navigating away discards it with no prompt from either guard —
  // silent loss of user input. `pendingCardChanges` is the same shape of
  // out-of-band dirtiness.
  dirty.value = currentData !== originalData.value || hasCardChanges || hasStagedFiles()
}

function getPropertyDef(property: string): PropertyDef | undefined {
  return entityType.value?.properties[property]
}

// Warn before browser tab close / hard reload / external navigation. Browsers
// require this to be the native dialog — they ignore custom UI here — so this
// stays as-is even though the in-app navigation guard below uses ConfirmModal.
function handleBeforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value) {
    e.preventDefault()
    e.returnValue = ''
  }
}

// Cmd/Ctrl+Enter: on the last (or only) step it submits (create); on an earlier
// wizard step it advances — matching the visible Create-only-on-last-step
// affordance rather than submitting from the middle of a wizard. No-op in edit
// mode (autosave; handleSubmit early-returns).
function handleKeydown(e: KeyboardEvent) {
  if (!((e.metaKey || e.ctrlKey) && e.key === 'Enter')) return
  e.preventDefault()
  if (!isEdit.value && wizard.isMultiStep.value && !wizard.isLastStep.value) {
    handleNext()
  } else {
    handleSubmit()
  }
}

// Lifecycle & Navigation Guards
onMounted(async () => {
  // Setup event listeners
  window.addEventListener('beforeunload', handleBeforeUnload)
  // Embedded: the host modal owns Cmd+Enter. This listener is document-level
  // with no target check, so two mounted forms would BOTH act on one keypress
  // — the modal submitting while the page form behind it also submits (or
  // silently advances a wizard step). beforeunload above is left registered:
  // duplicate handlers there just mean the browser warns if either form is
  // dirty, which is correct for a nested draft.
  if (!props.embedded) {
    document.addEventListener('keydown', handleKeydown)
  }

  // return_to is honoured in both modes — read it eagerly. Skipped when
  // embedded: a nested form inherits the HOST page's query string, so it would
  // otherwise adopt the parent's return_to (and, below, its prop.*/rel.*/link_*
  // pre-fill) as if they were its own.
  if (!props.embedded) {
    applyReturnToFromQuery()
  }

  // Load form data
  loading.value = true
  if (isEdit.value) {
    await loadEntity()
  } else {
    initializeDefaults()
    await loadTemplates()
    // After templates, so a duplicate's copied values win over the template's
    // defaults: the user asked for a copy of THIS entity, not for a fresh one.
    applyEmbeddedPrefill()
    loadState.value = 'loaded' // create mode: no entity to fetch
  }
  loading.value = false

  // Re-seed the wizard step from `?step=` now that entity/defaults are loaded:
  // a step whose `visible_when` needs loaded data is only in `visibleSteps`
  // after this point, so the construction-time seed may have clamped a
  // deep-link to the wrong step (RR-TXMU6).
  wizard.seedFromUrl()

  // TKT-3I5U: derive the staged entity's initial affordances from the
  // dry-run so the first affordance-filtered paint reflects defaults +
  // template values. Awaited so `stagedAffordancesReady` flips before
  // the user can interact, avoiding a hidden-field flash (F19).
  if (!isEdit.value) {
    await refreshStagedAffordances()
  }

  // TKT-E6094: mount the autosave composable in edit mode. The save
  // path replaces handleSubmit's Save button for edit forms; create
  // forms keep the explicit submit.
  if (isEdit.value && props.entityId && formConfig.value) {
    const inverseToCanonical = new Map<string, string>()
    for (const f of fields.value) {
      if (!f.relation) continue
      const inv = schemaStore.getInverseName(f.relation)
      if (inv) inverseToCanonical.set(inv, f.relation)
    }
    _autoSaveInstance.value = useAutoSave({
      getEntityType: () => formConfig.value!.entity,
      getEntityId: () => props.entityId!,
      formData,
      contentRef: content,
      // Seed the merge base from the load that already happened above
      // (TKT-DPBQ7S). `loadEntity` runs before this construction, so the
      // snapshot is parked rather than recorded; hand it over here.
      ...(_pendingServerSnapshot ? { initialServerSnapshot: _pendingServerSnapshot } : {}),
      inverseToCanonical,
      buildRelationsBody: () => buildAutoSaveRelationsBody(),
      applyServerProperty: (property, value) => {
        if (value === undefined) {
          delete formData.value[property]
        } else {
          formData.value[property] = value
        }
        // A committed state-machine move changes which transitions are now
        // performable — the loaded `_transitions` reflect the PRE-move state
        // (RR-NI145G). The PATCH response's fresh `_transitions` isn't threaded
        // through the autosave merge, so reload the entity to refresh the
        // status control (mirrors onAttachmentChanged's reload for
        // `_attachments`). Fire-and-forget: this is a UI-hint refresh, and the
        // write already succeeded.
        if (property in transitions.value) {
          void loadEntity(true)
        }
      },
      applyServerContent: (c) => {
        content.value = c
      },
      onError: (msg) => uiStore.error(msg),
    })
    // Consumed by the constructor above; later loads go through
    // `recordServerSnapshot` on the live instance instead.
    _pendingServerSnapshot = null
    // Register with the dirty registry so SSE-driven re-fetches in
    // other forms on the same entity preserve this form's dirty state.
    // The cleanup runs from the top-level onBeforeUnmount below —
    // registering a lifecycle hook after an `await` has no active
    // instance, so Vue would silently drop it and leak the registration.
    unregisterDirtyForm = registerForm(
      props.entityId,
      (property) => _autoSaveInstance.value?.isDirty(property) ?? false
    )
  }

  // TKT-GFQK pre-flight: a `direction: incoming` widget on a relation
  // without an `inverse:` declared in the metamodel can't be saved
  // through the unified PATCH. Warn the user at form-load time so the
  // failure surfaces before edits accumulate. The widget still renders
  // (display path is direction-aware and works), but save will throw
  // a clear error from buildRelationsPatch if they try.
  for (const f of fields.value) {
    if (f.relation && f.direction === 'incoming') {
      const inverse = schemaStore.getInverseName(f.relation)
      if (!inverse) {
        uiStore.warning(
          `Relation '${f.relation}' has no 'inverse:' declared in the metamodel. ` +
            `Saving changes from this widget will fail until the metamodel is updated.`
        )
      }
    }
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', handleBeforeUnload)
  document.removeEventListener('keydown', handleKeydown)
  unregisterDirtyForm?.()
  unregisterDirtyForm = null
  // TKT-3I5U: cancel any pending / in-flight staged dry-run, and mark
  // the component as gone so a response that has already arrived (but
  // is awaiting the microtask queue) doesn't write to dead refs
  // (RR-2PZB).
  stagedUnmounted = true
  if (stagedDryRunTimer) clearTimeout(stagedDryRunTimer)
  stagedDryRunController?.abort()
  // Supersede any proposal still awaiting a dialog. `useConfirm` resolves its
  // pending promise on APP-shell unmount, not on ours, so a route change with
  // the dialog open would otherwise resume the awaited continuation on a dead
  // component and schedule a real PATCH against a form the user has left.
  formGeneration.value++
})

// Returning a promise from the guard preserves the original navigation's
// push/replace semantics and popstate cursor — `next(false) + router.push(...)`
// would corrupt history for back/forward and any internal `router.replace`.
//
// dirty.value=false is set before returning ok. This is safe in this app
// because there are no global beforeResolve guards that could cancel the
// navigation downstream — if one were added, the assignment should move into
// a router.afterEach hook gated on success.
//
// Registration is skipped entirely when embedded. Two guards on one route
// record both fire on a single navigation, and both call the SINGLETON
// `confirm()` — whose in-flight promise is shared, so one dialog's answer is
// returned to both callers. A "Leave" meant for the page would silently
// discard the nested draft too. `props.embedded` is never reactive after
// mount, so this setup-time conditional is sound; the host modal owns
// discard-confirmation for the nested form.
if (!props.embedded) {
  onBeforeRouteLeave(async () => {
    // A clear-confirm dialog is open. `useConfirm` is a singleton that hands a
    // concurrent caller the SAME in-flight promise, so prompting here would
    // return that dialog's answer: the user clicking "Clear" would also be
    // answering "Leave anyway" without ever seeing the question. Block the
    // navigation instead and let them finish the decision in front of them.
    if (confirmingClear.value) return false

    // TKT-E6094: in edit mode, flush autosave before navigating away.
    // On clean commit we proceed silently; on error or timeout we
    // prompt the user to confirm.
    if (autoSave.value) {
      // Same reason as on submit: a body change inside the editor's debounce
      // window is not in `content` yet, so committing without flushing would
      // leave it behind on navigation.
      flushEditor()
      await nextTick()
      const result = await autoSave.value.commitImmediately()
      if (result.settled && !result.error) {
        dirty.value = false
        return true
      }
      return await confirm({
        title: 'Unsaved changes',
        message: result.error ?? 'Some changes are still saving.',
        confirmLabel: 'Leave anyway',
        danger: true,
      })
    }
    // Create-mode / no autosave: original prompt.
    if (!dirty.value) return true
    const ok = await confirm({
      title: 'Unsaved changes',
      message: 'You have unsaved changes. Are you sure you want to leave?',
      confirmLabel: 'Leave',
      danger: true,
    })
    if (ok) dirty.value = false
    return ok
  })
}

/**
 * Surface for a host that renders this form inside its own chrome (the
 * inline-create modal).
 *
 * The host needs to know whether there is unsaved input and to trigger a
 * submit. Both must be ASKED FOR, not inferred: sniffing bubbling `input` /
 * `change` events off a wrapper element misses every non-native widget — a
 * relation-picker selection, a wizard step, and the CodeMirror-backed markdown
 * body all emit Vue events that do not bubble as DOM events, so a form with a
 * written body would read as pristine and be discarded without a prompt.
 */
defineExpose({
  /** True when the user has entered anything not yet persisted. */
  isDirty: () => dirty.value,
  // Test seam (TKT-7YHKD1): the dirty BASELINE, so a test can assert the
  // reset re-seeded it after the awaited dry-run rather than before. `dirty`
  // itself only recomputes on the next edit, so it lags and cannot show this.
  _originalData: () => originalData.value,
  /** True while a create request is in flight. */
  isSaving: () => saving.value,
  /** Submit the form, exactly as the Create button does. */
  submit: () => handleSubmit(),
})
</script>

<template>
  <div
    v-if="formConfig"
    class="form-layout"
    :class="{ 'with-sidepanel': isEdit }"
    :data-testid="`form-state-${loadState}`"
  >
    <div class="dynamic-form" :class="{ embedded }">
      <!-- The host modal supplies its own title and help affordance, so the
           page header would be a duplicate heading inside the dialog. -->
      <header v-if="!embedded" class="form-header mobile-topbar">
        <h1>{{ title }}</h1>
        <button
          type="button"
          class="help-btn"
          title="Show help for this entity type"
          @click="helpModalOpen = true"
        >
          ?
        </button>
      </header>

      <!-- Template selector (create mode only) -->
      <div v-if="!isEdit && templates.length > 1" class="template-selector">
        <button
          v-for="tpl in templates"
          :key="tpl.name"
          type="button"
          class="template-pill"
          :class="{ active: selectedTemplate === tpl.name }"
          @click="selectTemplate(tpl.name)"
        >
          {{ getTemplateLabel(tpl.name) }}
        </button>
      </div>

      <div v-if="showBlockLoader" class="loading-state">
        <div class="spinner" />
        <span>Loading…</span>
      </div>
      <div v-else-if="loading" class="form-loading-placeholder" />

      <div v-else-if="notEditable" class="not-editable-state">
        <h2>This entity is not editable</h2>
        <p v-if="notEditableNote">{{ notEditableNote }}</p>
        <p v-else>
          Your current permissions don't allow updating
          <code>{{ entityId }}</code
          >. Return to the entity view to see available actions.
        </p>
        <router-link
          v-if="formConfig && entityId"
          :to="`/entity/${formConfig.entity}/${entityId}`"
          class="btn btn-secondary"
        >
          ← Back to entity
        </router-link>
      </div>

      <!-- `handleSubmit('navigate')`, not a bare reference: the DOM passes the
           SubmitEvent as the first argument, which would land in `mode`. -->
      <form v-else @submit.prevent="handleSubmit('navigate')">
        <div v-if="showReadOnlyID" class="form-field id-field">
          <label>ID</label>
          <div class="id-display">{{ entityId }}</div>
          <p class="field-help">IDs cannot be changed here; use rename.</p>
        </div>
        <div v-if="showManualIDInput" class="form-field id-field">
          <label>ID <span class="required">*</span></label>
          <input v-model="manualId" type="text" required placeholder="Unique ID..." />
        </div>
        <div v-if="showPrefixPicker" class="form-field id-field">
          <label>Prefix <span class="required">*</span></label>
          <select v-model="selectedPrefix" required>
            <option v-for="p in prefixOptions" :key="p" :value="p">{{ p }}</option>
          </select>
        </div>

        <!-- Every form renders through the same step model. A single-page (flat)
             form is one implicit, title-less step, so the stepper bar only shows
             when there is more than one step. -->
        <ol v-if="wizard.isMultiStep.value" class="wizard-steps" aria-label="Form steps">
          <li v-for="(step, sIdx) in wizard.visibleSteps.value" :key="sIdx">
            <button
              type="button"
              class="wizard-step-pill"
              :class="{
                active: sIdx === wizard.currentStep.value,
                done: sIdx < wizard.currentStep.value,
                'has-errors': stepsWithErrors.has(sIdx),
              }"
              :aria-current="sIdx === wizard.currentStep.value ? 'step' : undefined"
              @click="handleStepClick(sIdx)"
            >
              <span class="wizard-step-num">{{ stepsWithErrors.has(sIdx) ? '!' : sIdx + 1 }}</span>
              <span class="wizard-step-title">{{ step.title }}</span>
            </button>
          </li>
        </ol>

        <div
          v-if="wizard.currentStepDef.value"
          class="form-section"
          :class="{ 'wizard-panel': wizard.isMultiStep.value }"
        >
          <h2 v-if="wizard.currentStepDef.value.title">
            {{ wizard.currentStepDef.value.title }}
          </h2>
          <p v-if="wizard.currentStepDef.value.description" class="section-description">
            {{ wizard.currentStepDef.value.description }}
          </p>
          <div class="form-fields">
            <FormFieldList
              :key="fieldsRenderKey"
              :fields="visibleStepFields(wizard.currentStepDef.value)"
              :entity-type="formConfig.entity"
              :entity-id="entityId"
              :form-data="formData"
              :relations="relations"
              :errors="errors"
              :relation-affordances="relationAffordances"
              :attachments="attachments"
              :staged-files="stagedFiles"
              :save-generation="saveGeneration"
              :get-property-def="getPropertyDef"
              :is-field-readonly="isFieldReadonly"
              :readonly-reason-for="readonlyReasonFor"
              :option-verdicts-for="optionVerdictsFor"
              :transitions-for="transitionsFor"
              @update-field="proposeChange"
              @attachment-changed="onAttachmentChanged"
              @update-staged-files="updateStagedFiles"
              @update-relation="updateRelation"
              @update-relation-types="updateRelationTypes"
              @incoming-changed="updateIncomingPicker"
              @cards-changed="updateRelationCards"
            />
          </div>
        </div>

        <!-- Degenerate config: every step is conditionally hidden right now. -->
        <p v-else class="section-description">No fields to display.</p>

        <!-- Content field (markdown body). Shown on the final (or only) step. -->
        <div v-if="wizard.isLastStep.value" class="form-field content-field">
          <label for="content">Content</label>
          <p v-if="!contentWritable" class="content-owned-hint">
            This content is kept in sync with an external system; change it there.
          </p>
          <MarkdownEditor
            ref="markdownEditorRef"
            :model-value="content"
            :ref-resolver="refResolver"
            :mention-self="mentionSelf"
            :readonly="!contentWritable"
            placeholder="Markdown content..."
            @update:model-value="updateContent"
          />
        </div>

        <!-- TKT-7YHKD1: polite status for "Create & add another", whose whole
             effect (form blanks, toast appears) is otherwise visual only.
             Always rendered so the region exists before it is populated —
             a live region injected at the same moment as its text is not
             reliably announced. -->
        <p class="form-sr-only" role="status" aria-live="polite">{{ statusAnnouncement }}</p>

        <!-- Submit-time validation summary (create only — edit autosaves and has
             no submit gate). Announced via role=alert. -->
        <p v-if="!isEdit && errorCount > 0" class="wizard-error-summary" role="alert">
          ⚠ {{ errorCount }}
          {{ errorCount === 1 ? 'field needs' : 'fields need' }} attention<template
            v-if="wizard.isMultiStep.value && stepsWithErrors.size > 0"
          >
            — see the flagged step{{ stepsWithErrors.size === 1 ? '' : 's' }} above</template
          >.
        </p>

        <!--
          Actions branch on MODE, not on wizard-ness, so a wizard and a flat form
          behave identically:
          - EDIT: autosave per field, no Save button (the route guard flushes
            pending edits on leave); step Back/Next only when multi-step.
          - CREATE: Cancel + Back/Next (when multi-step) + Create on the last step.
        -->
        <div v-if="wizard.currentStepDef.value" class="form-actions mobile-actionbar">
          <!-- Leave-the-form control (autosave Back in edit, Cancel in create). -->
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="!autoSave && saving"
            @click="handleCancel"
          >
            {{ autoSave ? 'Back' : 'Cancel' }} <kbd>Esc</kbd>
          </button>

          <!-- Step navigation (multi-step only). -->
          <template v-if="wizard.isMultiStep.value">
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="wizard.isFirstStep.value"
              @click="handleBack"
            >
              ← Prev step
            </button>
            <button
              v-if="!wizard.isLastStep.value"
              type="button"
              class="btn btn-primary"
              @click="handleNext"
            >
              Next step →
            </button>
          </template>

          <!-- Create only: the commit button, on the last (or only) step. -->
          <PendingButton
            v-if="!isEdit && wizard.isLastStep.value"
            type="submit"
            class="btn btn-primary"
            :pending="saving"
            label="Create"
            pending-label="Saving…"
          >
            <template #adornment><kbd>&#8984;&#8629;</kbd></template>
          </PendingButton>

          <!--
            TKT-7YHKD1: second terminal outcome for the create path — create,
            then blank the form for the next record instead of navigating.

            `type="button"`, not submit: two submit buttons in one form make
            Enter ambiguous, and @submit.prevent must keep meaning the primary
            Create. Hidden when embedded — an inline-create host is waiting for
            exactly one entity to link, so "add another" has no meaning there.
          -->
          <PendingButton
            v-if="!isEdit && !embedded && wizard.isLastStep.value"
            type="button"
            class="btn btn-secondary"
            :pending="saving"
            label="Create & add another"
            pending-label="Saving…"
            @click="handleSubmit('again')"
          />

          <!-- Edit: ambient autosave status stands in for a Save button. -->
          <AutoSaveIndicator
            v-if="autoSave"
            :status="autoSave.status"
            :error="autoSave.lastError"
          />
        </div>
      </form>
    </div>

    <!-- Side panel for edit mode -->
    <SidePanel v-if="isEdit && entityId" :form-id="formId" :entity-id="entityId" />
  </div>

  <div v-else class="error-state">
    <h2>Form not found</h2>
    <p>The form "{{ formId }}" does not exist in the configuration.</p>
  </div>

  <!-- Help Modal -->
  <HelpModal
    v-if="formConfig"
    :open="helpModalOpen"
    :entity-type="formConfig.entity"
    :entity-label="entityType?.label"
    @close="helpModalOpen = false"
  />
</template>

<style scoped>
.form-layout {
  display: flex;
  gap: 24px;
}

.form-layout.with-sidepanel .dynamic-form {
  flex: 1;
  min-width: 0;
}

.dynamic-form {
  max-width: 800px;
  min-width: 500px;
  width: 100%;
}

/* Embedded: the host dialog owns the width. The page min-width would
   otherwise force the modal wider than a narrow viewport allows. */
.dynamic-form.embedded {
  max-width: none;
  min-width: 0;
}

.form-header {
  margin-bottom: 24px;
  display: flex;
  align-items: center;
  gap: 12px;
}

.form-header h1 {
  margin: 0;
}

.help-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  background: var(--bg-color);
  border: 1px solid var(--border-color);
  border-radius: 50%;
  font-size: 14px;
  font-weight: 600;
  color: var(--muted-text);
  cursor: pointer;
  transition: all 0.15s;
}

.help-btn:hover {
  background: var(--accent-color, #6366f1);
  border-color: var(--accent-color, #6366f1);
  color: white;
}

/* Uses global .loading-state and .spinner from App.vue */

.form-section {
  background: var(--card-bg);
  border-radius: 8px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
  padding: 24px;
  margin-bottom: 24px;
}

.form-section h2 {
  margin: 0 0 8px;
  font-size: 18px;
}

.section-description {
  color: var(--muted-text);
  margin-bottom: 24px;
}

/* Wizard stepper */
.wizard-steps {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  list-style: none;
  margin: 0 0 20px;
  padding: 0;
}

.wizard-step-pill {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 999px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  color: var(--muted-text);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition:
    border-color 0.15s,
    color 0.15s;
}

.wizard-step-pill:hover {
  border-color: var(--accent-color);
  color: var(--text-color);
}

.wizard-step-pill:focus-visible {
  outline: 2px solid var(--accent-color);
  outline-offset: 2px;
}

/* "You are here" = a filled pill. This is the primary cue and is INDEPENDENT
   of the error color, so an active step still reads as active even when it also
   has an error (see .active.has-errors below). */
.wizard-step-pill.active {
  background: var(--accent-color);
  border-color: var(--accent-color);
  color: #fff;
  font-weight: 600;
}

.wizard-step-pill.done {
  color: var(--text-color);
}

.wizard-step-num {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: var(--border-color);
  font-size: 12px;
}

.wizard-step-pill.done .wizard-step-num {
  background: var(--accent-color);
  color: #fff;
}

/* On the filled active pill, the number badge is inverted (light on accent). */
.wizard-step-pill.active .wizard-step-num {
  background: #fff;
  color: var(--accent-color);
}

/* An errored step: red outline + red number badge, but NOT active. */
.wizard-step-pill.has-errors:not(.active) {
  border-color: var(--error-color);
  color: var(--error-color);
}

.wizard-step-pill.has-errors:not(.active) .wizard-step-num {
  background: var(--error-color);
  color: #fff;
}

/* The active step that also has an error: filled with the error color, so it
   still reads as "you are here" while signalling the problem. */
.wizard-step-pill.active.has-errors {
  background: var(--error-color);
  border-color: var(--error-color);
  color: #fff;
}

.wizard-step-pill.active.has-errors .wizard-step-num {
  background: #fff;
  color: var(--error-color);
}

.wizard-error-summary {
  color: var(--error-color);
  font-size: 13px;
  font-weight: 600;
  margin: 0 0 12px;
}

/* Same 12-column layout grid as the detail page (TKT-5V8704), so `span:` in
 * data-entry.yaml means the same thing on a form as in a view section. Forms
 * were already single-column via flex-column; the grid preserves that for
 * unspanned fields while letting an author group related ones onto a row. */
.form-fields {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 20px var(--space-xl);
  /* Top-align, don't stretch. Grid items default to `stretch`, which makes
     every field in a row as tall as the tallest — so one field with a
     transitions panel or a long help text under it leaves its neighbours
     with a void beneath their input. Aligning to the start keeps each
     control tight to its own label. */
  align-items: start;
}

/* EVERY direct child spans the full 12 by default, not just `.form-field`.
 * FormFieldList also emits RelationCards / RelationPicker, which carry their
 * own root class — without this they'd become auto-width grid items and the
 * whole form would collapse into narrow columns.
 *
 * The var() fallback (not a bare `span 12`) is load-bearing: `.form-fields > *`
 * and `.form-field` have equal specificity, so a plain `span 12` here would win
 * on source order and silently swallow every authored span. Reading the same
 * custom property means both rules agree, and the ONE default lives in the
 * fallback. */
.form-fields > * {
  grid-column: span var(--field-span, 12);
  min-width: 0;
}

.form-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  /* Grid items floor at min-content without this; a long placeholder or
     option label would push its track wider than its share. */
  min-width: 0;
}

@media (max-width: 640px) {
  .form-fields {
    grid-template-columns: minmax(0, 1fr);
  }

  .form-fields > *,
  .form-field {
    grid-column: span 1;
  }
}

.form-field label {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-color);
}

.id-field {
  margin-bottom: 16px;
}

.id-field input,
.id-field select {
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  font-size: 14px;
  background: var(--input-bg);
  color: var(--text-color);
}

.id-display {
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  font-size: 14px;
  background: var(--input-bg);
  color: var(--muted-text);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.required {
  color: var(--error-color, #ef4444);
  margin-left: 2px;
}

.field-help {
  font-size: 12px;
  color: var(--muted-text);
  margin: 0;
}

.content-field {
  margin-top: 16px;
  margin-bottom: 24px;
}

.content-owned-hint {
  margin: 0 0 var(--space-xs);
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}

/* Visually hidden but exposed to assistive tech (the standard sr-only clip,
   matching AutoSaveIndicator's). Carries the TKT-7YHKD1 create+reset status. */
.form-sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  padding-top: 24px;
}

/* Uses global .btn, .btn-primary, .btn-secondary from App.vue */

.error-state {
  padding: 48px;
  text-align: center;
  color: var(--muted-text);
}

.error-state h2 {
  color: var(--error-color, #ef4444);
}

.template-selector {
  display: flex;
  gap: 8px;
  margin-bottom: 20px;
}

.template-pill {
  padding: 8px 16px;
  border-radius: 20px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  border: 1px solid var(--border-color, #e2e8f0);
  background: var(--bg-color, #f8fafc);
  color: var(--text-color, #1e293b);
  transition: all 0.15s;
}

.template-pill:hover {
  border-color: var(--accent-color, #6366f1);
  background: var(--card-bg);
}

.template-pill.active {
  background: var(--accent-color, #6366f1);
  border-color: var(--accent-color, #6366f1);
  color: white;
}

@media (max-width: 768px) {
  .form-layout {
    flex-direction: column;
    gap: 12px;
  }

  .dynamic-form {
    min-width: 0;
    max-width: none;
  }

  .form-section {
    padding: 0;
    margin-bottom: 16px;
    border: none;
    box-shadow: none;
    background: none;
  }

  .content-field {
    padding: 0;
    margin-top: 8px;
    margin-bottom: 12px;
  }

  /* Sticky topbar styles live in src/styles/mobile-bars.css
     (.mobile-topbar). Override only the bottom margin here — the form
     wants 12px between bar and first field instead of the default 16. */
  .form-header {
    margin-bottom: 12px;
  }

  .form-header h1 {
    font-size: 20px;
  }

  /* .form-actions uses .mobile-actionbar from mobile-bars.css. */

  .form-actions .btn {
    flex: 1;
    justify-content: center;
    min-height: 44px;
  }

  .template-selector {
    flex-wrap: wrap;
    gap: 6px;
  }
}
</style>
