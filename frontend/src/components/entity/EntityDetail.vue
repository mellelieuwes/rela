<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, onUnmounted } from 'vue'
import { RouterLink, useRoute, useRouter, type RouteLocationRaw } from 'vue-router'
import { useSchemaStore, useUIStore } from '@/stores'
import { useScopeNavigation } from '@/composables'
import { useBackTarget } from '@/composables/useBackTarget'
import { isCancelledFetch } from '@/composables/usePageData'
import { fetchView, getCommands, getErrorMessage } from '@/api'
import { runAction } from '@/api/actions'
import { useActionFeedback } from '@/composables/useActionFeedback'
import { useWorld, worldQuery, DEFAULT_WORLD } from '@/composables/useWorld'
import { entityRef, refBareId, refFace } from '@/utils/entityRef'
import { worldText, type WorldTextVars } from '@/utils/worldText'
import type { ViewEntity, ViewResponse, ViewSection, ViewSectionField, ViewTreeNode } from '@/api'
import type { Entity } from '@/types'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { useAutoSave } from '@/composables/useAutoSave'
import { toggleCheckboxInSource } from '@/utils/checkboxToggle'
import type { ActionConfig, Command } from '@/types'
import { getEditFormId } from '@/types'
import { entityDetailHref } from '@/utils/entityRoute'
import { shouldDeferToBrowser } from '@/utils/openIntent'
import { computeActionAllowed } from '@/utils/affordancesWarning'
import { isInputFocused } from '@/utils/dom'
import { isAnyModalOpen } from '@/composables/modalStack'
import {
  renderMarkdown,
  renderMermaidDiagrams,
  renderPlantUMLDiagrams,
  getCheckboxStats,
  type EntityRefResolver,
} from '@/utils/markdown'
import { makeRefResolver } from '@/utils/entityRefResolver'
import BackButton from '@/components/common/BackButton.vue'
import Badge from '@/components/common/Badge.vue'
import InaccessibleField from '@/components/common/InaccessibleField.vue'
import PropertyDisplay from '@/components/common/PropertyDisplay.vue'
import type { PropertyItem } from '@/components/common/PropertyDisplay.vue'
import { ChevronRight } from 'lucide-vue-next'
import { defaultRegistry } from '@/widgets/registry'
import { isDenseEmpty, viewFieldRoutingHint } from '@/widgets/viewRouting'
import type { WidgetRoutingHint } from '@/widgets/types'
import type { PropertyDef } from '@/types'
import type { Component } from 'vue'
import DocumentsPanel from '@/components/entity/DocumentsPanel.vue'
import CommentsPanel from '@/components/entity/CommentsPanel.vue'
import CommentIndicator from '@/components/entity/CommentIndicator.vue'
import TextSelectionComment from '@/components/entity/TextSelectionComment.vue'
import TextCommentPopover from '@/components/entity/TextCommentPopover.vue'
import BlockCommentOverlay from '@/components/entity/BlockCommentOverlay.vue'
import { acceptComment, listComments, type Comment } from '@/api/comments'
import { shouldFlipPopover } from '@/utils/popoverFlip'
import { applyHighlights, type HighlightRange } from '@/utils/commentHighlight'
import CommandModal from '@/components/entity/CommandModal.vue'
import ExportMenu from '@/components/entity/ExportMenu.vue'
import SectionCreateButton from '@/components/entity/SectionCreateButton.vue'
import InlineCreateFormModal from '@/components/forms/InlineCreateFormModal.vue'
import { buildCreateLinkQuery } from '@/utils/createLink'
import type { ViewSectionCreate, ViewSectionCreateTarget } from '@/api/views'
import { entityExportUrl } from '@/api/transforms'
import CopyMenu from '@/components/entity/CopyMenu.vue'
import FaceMenu from '@/components/entity/FaceMenu.vue'
import DuplicateModal from '@/components/entity/DuplicateModal.vue'
import WorldBadge from '@/components/entity/WorldBadge.vue'
import WorldBanner from '@/components/common/WorldBanner.vue'
import { invokeCopy } from '@/api/copies'
import type { CopyOffer, Face } from '@/types'
import SectionEditForm, { type SectionEditField } from '@/components/forms/SectionEditForm.vue'
import AutoSaveIndicator from '@/components/forms/AutoSaveIndicator.vue'
import {
  buildSectionEditFields as buildSectionEditFieldsPure,
  sectionShouldRouteToInlineEdit as sectionShouldRouteToInlineEditPure,
  applyPropertyToEntry,
  applyPropertyToRow,
  rowShouldRouteToInlineEdit as rowShouldRouteToInlineEditPure,
} from './sectionEditFields'
import type { AutoSaveErrorInfo } from '@/composables/useAutoSave'
import { useConfirm, withConfirmError } from '@/composables/useConfirm'
import { useDelayedPending } from '@/composables/useDelayedPending'
import { beginRouteLoad } from '@/composables/useNavigationPending'
import { PENDING_TIMINGS } from '@/composables/pendingTimings'
import { recordRecentEntity } from '@/utils/recentEntities'

const props = withDefaults(
  defineProps<{
    entityType: string
    entityId: string
    /**
     * Hide the header action toolbar (commands, Edit, History, Delete).
     *
     * For embedding this component as a READ surface — the calendar's preview
     * modal — where the host supplies its own actions. Without it the modal
     * shows two Edit buttons and a destructive Delete one click from a
     * calendar chip, which is not what a preview is for.
     *
     * Suppresses the buttons only. Permission checks, keyboard handlers and
     * every other behaviour are untouched, so this cannot widen what a
     * principal may do.
     */
    hideActions?: boolean
  }>(),
  { hideActions: false }
)

const router = useRouter()
const route = useRoute()
const schemaStore = useSchemaStore()
const uiStore = useUIStore()
const { confirm } = useConfirm()
// The world is read from the URL, the same source EntityList reads, so a
// detail page reached from a world-bound list stays in that world and a
// deep link round-trips.
const { world, isWorldBound, worldParam, setWorld } = useWorld()

// --- No face in the requested world -------------------------------------
//
// The entity exists and this caller may read it; the world they asked for
// simply has no face of it. That is the ordinary state of every unpublished
// draft, and it used to render as "Error — entry entity not found", which sent
// people off to create the thing a second time.
//
// The server answers 200 with `_world_absent` and the DEFAULT face, so this
// page shows real content with a banner explaining which world is missing it
// and how to get to the face that exists. Crucially the page is NOT read-only
// in this state — what is on screen is the default face, addressed by its
// bare id, so the ordinary world banner (which announces read-only-ness)
// must not render alongside it.
const worldAbsent = computed(() => viewData.value?._world_absent === true)

// --- The address of the row on screen -------------------------------------
//
//     view[entity@face]  --Edit-->  form[entity@face]  --Save-->  PATCH entity@face
//
// Every write this page makes goes to `servedRef`: the coordinate the server
// reports in the entry's `_self`, face included. Under a world the bare id
// resolves to whichever face the world picks, so the bare id is not a stable
// address for what is on screen — `_self` is, and the server accepts it on
// GET, PATCH and DELETE alike (TKT-SLFURL). Whether a write is ALLOWED is
// `_actions`, which the server computes for that same face. Together those
// two replace the page-level "read-only under a world" lock this component
// used to apply: that lock re-derived a decision the server had already made,
// got it wrong for every type without faces and for every chain hit on the
// bare face, and contradicted the very `_actions` it was overriding (atlas
// worlds issues 2, 3, 4, 10).
//
// Before the entry has loaded, the route's own id is the best address there
// is; it may itself carry a face (`/entity/policy/POL-1@published`).
const servedRef = computed(() => (entry.value ? entityRef(entry.value) : props.entityId))
// The face on screen, '' for the bare face. A fact read off the response,
// never derived from the world.
const servedFace = computed(() => refFace(servedRef.value))
// The bare id, for surfaces addressed per ENTITY rather than per row:
// documents, history, scope navigation. NOT commands — a command acts on the
// face on screen and takes servedRef (BUG-G2BASF).
const bareEntityId = computed(() => refBareId(props.entityId))

// Scope navigation (prev/next within a list) and back affordance
// (return_to / from precedence). Two parallel concerns: scope-nav walks
// a list; backTarget answers "where do I go back to". Both can be active
// at once.
// Scope navigation is per ENTITY (`_position` walks a list by bare id).
const { scopeNav, loadScopeNav, scopeTarget, navigateScope } = useScopeNavigation(
  () => bareEntityId.value
)
const backTarget = useBackTarget()

// State
const loading = ref(true)
const error = ref<string | null>(null)

// pageState mirrors DynamicForm's `form-state-*` contract: a stable signal
// that this screen has finished resolving, so a screenshot can wait for it
// rather than hanging until its capture timeout.
const pageState = computed<'pending' | 'loaded' | 'error'>(() => {
  if (error.value) return 'error'
  return loading.value ? 'pending' : 'loaded'
})
const viewData = ref<ViewResponse | null>(null)
const loadedCommands = ref<Command[]>([])

// Commands run against the ADDRESS on screen, face included (BUG-G2BASF).
//
// This used to suppress every command whenever a non-bare face was served, on
// the reasoning that the script would receive the bare face's content while
// the reader looked at another face. That reasoning described a discarded
// address, not a server limitation: both command contexts resolve an explicit
// `ID@face` literally — `entity` parses it (commands.go, ParseStateRef) and
// `view` short-circuits on `entry.Explicit` even under `defaultViewWorld()`
// (viewworld.go, viewEntry), where the face is additionally ACL-checked by
// faceReadable. The mismatch existed only because this component sent the bare
// id and then hid the button it had just made inaccurate.
//
// On a FACED type the suppression was total rather than partial — such a type
// has no bare face to fall back to (BUG-HC6I2T), so no address rendered the
// buttons at all.
const commands = computed<Command[]>(() => loadedCommands.value)

// Prev/next within a list re-fetches this component in place. Blanking the
// page to a centred spinner on every step was the worst layout shift in
// the app — the article collapsed to ~140px and sprang back — and on a
// fast connection it read as a flicker, not a loading state.
//
// Two rules apply, in order. (1) Keep previous content: while an entity is
// already on screen, stepping to a neighbour holds it until the next
// resolves, so nothing is shown at all. (2) On a genuine cold load, the
// gate still suppresses anything quicker than the navigation delay.
const showBlockLoader = useDelayedPending(() => loading.value && !viewData.value, {
  delay: PENDING_TIMINGS.navDelayMs,
  minDuration: PENDING_TIMINGS.navMinDurationMs,
})
const showOverflowMenu = ref(false)

const commandModalRef = ref<InstanceType<typeof CommandModal> | null>(null)

// historyTarget / editTarget back the header's History and Edit affordances.
// Both are pure navigation (no mutation), so they render as real links and
// support cmd/ctrl/middle-click — unlike Delete, which stays a <button>.
// Each is the single source of truth for its destination, shared with the
// keyboard shortcut so both routes agree.
//
// History is PER-FACE (`entity_versions` is keyed by content state), so the
// world has to ride along: dropping it sent the reader to the DEFAULT face's
// history from a world-bound page — a genuinely different record, presented as
// the right one with nothing on screen naming the face (BUG-2).
const historyTarget = computed<RouteLocationRaw>(() => ({
  path: `/history/${props.entityType}/${bareEntityId.value}`,
  query: worldParam.value ? { world: worldParam.value } : {},
}))

const contentRef = ref<HTMLElement | null>(null)

// Computed
const typeDef = computed(() => schemaStore.getEntityType(props.entityType))
const editFormId = computed(() => getEditFormId(schemaStore, props.entityType))

const entry = computed(() => viewData.value?.entry || null)
const entryTitle = computed(() => {
  if (!entry.value) return props.entityId
  return entityDisplayTitle(entry.value)
})

// inaccessibleByName maps each inaccessible field's name to its reason
// (e.g. 'git-crypt'), letting consumers render per-property tooltips
// without re-walking the array. Sourced from the view-rendered entry —
// the backend's entityToV1 (api_v1.go) attaches inaccessible[] when the
// underlying entity is locked.
const inaccessibleByName = computed<Map<string, string>>(() => {
  const m = new Map<string, string>()
  for (const f of entry.value?.inaccessible || []) {
    m.set(f.name, f.reason)
  }
  return m
})

const isInaccessible = computed(() => (entry.value?.inaccessible?.length ?? 0) > 0)

// Affordance gates: `_actions` map from the server. `false` → hide;
// anything else → render. See frontend/src/utils/affordancesWarning.ts.
const mayUpdate = computeActionAllowed(entry, 'update')
const mayDelete = computeActionAllowed(entry, 'delete')

// `_actions` alone. The server computes the map for the FACE it served, and
// every write from this page is addressed to that face (`servedRef`), so the
// verdict and the write agree by construction — under a world as in the
// default one. A stand-in face no grant covers reports `update: false`, and
// that is what hides Edit; a chain hit on the bare face reports `true`, and
// Edit shows. There is nothing left for the page to AND in.
const canUpdate = computed(() => mayUpdate.value)
const canDelete = computed(() => mayDelete.value)

// Duplicate is offered off `inline_create`, NOT `_actions` (TKT-Z8K2FS).
// `create` is a COLLECTION-scope verb, so an entity response carries only
// update/delete/rename and has no `create` key to read. The sidebar map is
// populated only when the principal may create the type AND a create form
// resolves for it — both conditions a duplicate needs — and it carries the
// resolved form id, so presence IS the affordance. A UI hint, never
// authorization: the create re-authorizes.
//
// Withheld on an inaccessible entity: its properties are ciphertext, so a
// "copy" of them would be a copy of nothing.
const duplicateFormId = computed(() =>
  isInaccessible.value ? undefined : schemaStore.inlineCreateFormFor(props.entityType)
)
const canDuplicate = computed(() => !!duplicateFormId.value && !!entry.value)
const showDuplicateModal = ref(false)

function handleDuplicated(created: { id: string; type: string }) {
  showDuplicateModal.value = false
  uiStore.showToast('success', `Created ${created.id}`)
  void router.push(`/entity/${created.type}/${created.id}`)
}

// Nil: undefined when editing is unavailable (no configured form, an
// inaccessible/git-crypt entity, or no update permission on the face on
// screen). The template then renders nothing rather than a link to a page
// that would refuse the write.
//
// The form opens on the ADDRESS of the row on screen, face included, so
// what you look at is what you edit is what you save.
const editTarget = computed<RouteLocationRaw | undefined>(() => {
  if (!editFormId.value || isInaccessible.value || !canUpdate.value) return undefined
  return { name: 'form-edit', params: { id: editFormId.value, entityId: servedRef.value } }
})

// The entry's content section gets a custom renderer (mermaid + interactive
// checkboxes) instead of the generic section render-path. Other content
// sections — content cards from configured views — use the generic path.
//
// Shared predicate so the section-finding (entryContentSection) and section-
// mutating (handleCheckboxToggle) paths stay in sync — drift between the two
// would silently update the wrong section's content.
function isEntryContentSection(s: { display?: string; hasContent?: boolean; content?: string }) {
  return s.display === 'content' && s.hasContent === true && !!s.content
}
const entryContentSection = computed(() => {
  const sections = viewData.value?.sections
  if (!sections) return null
  return sections.find(isEntryContentSection) || null
})

// Section ids offered as comment anchor targets (TKT-FIO205). `sectionId` is
// the operator-authored name from data-entry.yaml, which is why it is anchorable
// at all: it is a NAME, not an offset, so it survives edits to the entity body.
const commentSectionIds = computed(
  () => viewData.value?.sections?.map((s) => s.sectionId).filter(Boolean) ?? []
)

// ─── Per-field comments (TKT-FIO205) ─────────────────────────────────────
//
// Fetched ONCE per entity and grouped by anchor ref, not once per field: a
// per-field request would be N round-trips for N properties, and the server
// already returns the whole thread in one call.
const comments = ref<Comment[]>([])

const commentsEnabled = computed(
  () => schemaStore.getEntityType(props.entityType)?.commentable === true
)

const commentsByProperty = computed(() => {
  const byRef = new Map<string, Comment[]>()
  for (const c of comments.value) {
    if (c.anchor.kind !== 'property') continue
    const bucket = byRef.get(c.anchor.ref)
    if (bucket) bucket.push(c)
    else byRef.set(c.anchor.ref, [c])
  }
  return byRef
})

function commentsForProperty(name: string): Comment[] {
  return commentsByProperty.value.get(name) ?? []
}

/**
 * The entity id addressed for comments, carrying the resolved face.
 *
 * Comments are per content state (FEAT-9CD2MX): a remark on the draft is not a
 * remark on the published version. The view response reports which face the
 * world actually served, so the thread follows the content on screen rather
 * than always addressing the default face.
 */
const commentEntityId = computed(() => {
  const face = viewData.value?.entry?._world?.face
  return face ? `${props.entityId}@${face}` : props.entityId
})

async function loadComments() {
  if (!commentsEnabled.value) return
  try {
    comments.value = await listComments(props.entityType, commentEntityId.value)
  } catch {
    // A failure here means "cannot read the target, or commenting is off" —
    // the server makes those indistinguishable on purpose. Either way there is
    // no thread to show, and the page's primary content must still render, so
    // this degrades to "no comments" rather than surfacing an error.
    comments.value = []
  }
}

const checkboxStats = computed(() => {
  const c = entryContentSection.value?.content
  return c ? getCheckboxStats(c) : null
})

// refResolver wires the server-supplied mentions map into the markdown
// renderer so bare-ID code spans become titled in-app links. Null when
// the response carries no mentions; renderMarkdown then behaves exactly
// as before (no resolver, no rewrite).
// Shared with the editor via makeRefResolver, so a reference shows the same
// title whether it is being read or edited.
const refResolver = computed<EntityRefResolver | undefined>(() =>
  makeRefResolver(viewData.value?.mentions)
)

// Text-anchored comments as source ranges (TKT-FIO205 stage 2). Offsets are
// resolved server-side per read; a detached anchor has none and is simply not
// highlighted (it still shows in the panel).
const textHighlights = computed<HighlightRange[]>(() =>
  comments.value
    .filter((c) => c.anchor.kind === 'text' && c.anchor.start != null && c.anchor.end != null)
    .map((c) => ({
      id: c.id,
      start: c.anchor.start as number,
      end: c.anchor.end as number,
      uncertain: c.anchor.uncertain,
    }))
)

const renderedEntryContent = computed(() => {
  if (!entryContentSection.value) return ''
  // Marks are inserted into the SOURCE before rendering: the server's offsets
  // are source coordinates, and re-finding the text in the rendered DOM would
  // mean re-implementing the matcher against a document the renderer (and then
  // mermaid) has already transformed.
  const source = applyHighlights(entryContentSection.value.content || '', textHighlights.value)
  return renderMarkdown(source, {
    refResolver: refResolver.value,
    interactive: true,
  })
})

// Re-renders re-process mermaid diagrams inside the content body. Checkbox
// clicks are handled via delegation on contentRef (see contentClick), which
// doesn't need re-binding on every content swap.
//
// flush: 'post' so the watch fires after the v-html update has landed in the
// DOM — otherwise contentRef.value is the previous (or null) element and
// mermaid diagrams render on stale content. nextTick alone is not enough
// because the watch can fire in the same tick as loading→entry visibility.
watch(
  renderedEntryContent,
  async () => {
    if (contentRef.value) {
      await renderMermaidDiagrams(contentRef.value)
      renderPlantUMLDiagrams(contentRef.value, schemaStore.app?.plantuml_server_url)
    }
  },
  { flush: 'post' }
)

// Feeds the `@` menu's starting list (TKT-39TIB4). Only the address is kept;
// the menu reloads it through the read gate before showing it.
watch(
  () => (entry.value ? `${entry.value.type}\u0000${entry.value.id}` : null),
  () => {
    if (entry.value?.type) recordRecentEntity(refBareId(entry.value.id), entry.value.type)
  },
  { immediate: true }
)

// Content-only useAutoSave instance. EntityDetail does not own a form
// surface, so the property and relations channels are disabled —
// scheduleFieldSave/scheduleRelationsChange would throw if called. The
// content channel debounces at 100ms so rapid checkbox clicks coalesce
// into one PATCH while still feeling instant; the e2e suite tolerates
// up to 2s for the UI poll and 5s for the server poll.
//
// contentRef is a computed mirror of viewData.entry.content; the
// composable only reads its shape (never writes), so a read-only
// computed is sufficient. Property/relations callbacks are no-ops to
// satisfy AutoSaveOptions's required surface; they are unreachable
// because their channels are disabled and mergeServerResponse skips
// them.
const entryContent = computed(() => entry.value?.content ?? '')
const entryProperties = computed<Record<string, unknown>>(() => entry.value?.properties ?? {})
// When the route changes mid-debounce, the watch pins the previous
// entity identity here so the in-flight flush PATCHes the entity the
// user actually clicked, not the one they just navigated to.
const pinEntityForFlush = ref<{ type: string; id: string } | null>(null)
const contentAutoSave = useAutoSave({
  getEntityType: () => pinEntityForFlush.value?.type ?? props.entityType,
  // The row's ADDRESS, face included: a checkbox toggled on the published
  // face must land on the published face. The pin holds an address too.
  getEntityId: () => pinEntityForFlush.value?.id ?? servedRef.value,
  contentDebounceMs: 100,
  formData: entryProperties as unknown as import('vue').Ref<Record<string, unknown>>,
  contentRef: entryContent as unknown as import('vue').Ref<string>,
  inverseToCanonical: new Map(),
  buildRelationsBody: () => null,
  applyServerProperty: () => {},
  applyServerContent: (next) => {
    const view = viewData.value
    if (!view || !view.entry) return
    // If the route changed mid-flush, the previous entity's PATCH may
    // resolve after the new entity's loadView; reject the apply by
    // entity-identity rather than splice stale content into the new
    // view.
    const pinned = pinEntityForFlush.value
    if (pinned && (view.entry.id !== pinned.id || view.entry.type !== pinned.type)) return
    const nextSections = view.sections.map((s) =>
      isEntryContentSection(s) ? { ...s, content: next } : s
    )
    viewData.value = { ...view, entry: { ...view.entry, content: next }, sections: nextSections }
  },
  onError: (msg) => uiStore.error(msg),
  disablePropertyChannel: true,
  disableRelationsChannel: true,
})

// Accepting a suggestion (TKT-S5C0K3) writes the body on the server, so it must
// not race a pending checkbox save: that PATCH would carry the pre-accept body
// and undo the change, or fail its version check. Pending saves are flushed
// first, and toggles are refused while the accept is in flight.
const accepting = ref(false)
const canAccept = computed(
  () => canUpdate.value && !accepting.value && contentAutoSave.status.value !== 'saving'
)

async function acceptSuggestion(c: Comment) {
  if (!canAccept.value) return
  accepting.value = true
  try {
    // A save that timed out may still land on the server after the accept and
    // overwrite it with the pre-accept body, so an unsettled flush aborts.
    const flushed = await contentAutoSave.commitImmediately()
    if (!flushed.settled || flushed.error) {
      uiStore.error('Could not save pending changes; the suggestion was not applied')
      return
    }
    const res = await acceptComment(props.entityType, commentEntityId.value, c.id)
    const view = viewData.value
    if (view?.entry) {
      const nextSections = view.sections.map((s) =>
        isEntryContentSection(s) ? { ...s, content: res.content } : s
      )
      viewData.value = { ...view, entry: { ...view.entry, content: res.content }, sections: nextSections }
    }
    if (res.warnings && res.warnings.length > 0) {
      const codes = [...new Set(res.warnings.map((w) => w.code))].join(', ')
      uiStore.warning(`Suggestion applied with ${res.warnings.length} warning(s): ${codes}`)
    }
    closeTextComment()
    await loadView()
  } catch (err) {
    uiStore.error(`Failed to accept suggestion: ${getErrorMessage(err, 'unknown error')}`)
    await loadComments()
  } finally {
    accepting.value = false
  }
}

function contentClick(event: MouseEvent) {
  const target = event.target as HTMLElement | null

  // The chip beside a highlighted link opens that link's thread. Checked first
  // because it sits inside the mark it belongs to.
  const chip = target?.closest<HTMLElement>('[data-comment-chip]')
  if (chip) {
    event.preventDefault()
    openTextComment(chip.dataset.commentId, chip)
    return
  }

  // A LINK inside a highlight keeps its own click: navigation is the primary
  // action, and the chip above is how that thread is reached instead.
  if (target?.closest('a[href]')) return

  // Any other comment highlight opens its thread. Checked before the checkbox
  // branch because both are delegated from the same handler.
  const mark = target?.closest<HTMLElement>('mark[data-comment-id]')
  if (mark) {
    event.preventDefault()
    openTextComment(mark.dataset.commentId, mark)
    return
  }

  const checkbox = target?.closest<HTMLInputElement>('input[type="checkbox"][data-cb-idx]')
  if (!checkbox) return
  event.preventDefault()
  const raw = checkbox.dataset.cbIdx
  if (raw === undefined) return
  const idx = parseInt(raw, 10)
  if (Number.isNaN(idx)) return
  handleCheckboxToggle(idx)
}

/**
 * The text comment whose thread is open, plus where to anchor its popover.
 *
 * Held here rather than in the highlight itself: the marks live inside v-html
 * output, so there is no component per highlight to own the state.
 */
const openTextCommentId = ref<string | null>(null)
const textCommentAnchorEl = ref<HTMLElement | null>(null)

function openTextComment(id: string | undefined, el: HTMLElement) {
  if (!id) return
  // Clicking the open highlight again closes it, matching the field indicators.
  if (openTextCommentId.value === id) {
    closeTextComment()
    return
  }
  openTextCommentId.value = id
  textCommentAnchorEl.value = el
}

function closeTextComment() {
  openTextCommentId.value = null
  textCommentAnchorEl.value = null
}

/**
 * The whole thread at the clicked highlight — every comment resolving to the
 * same range, not just the one whose id is on the mark.
 *
 * Replies are separate comments sharing an anchor (stage 1 has no threading),
 * so they resolve to the SAME range and only the first one gets a mark. Keying
 * the popover on the mark's id alone showed a single comment and made a saved
 * reply look lost.
 */
const openTextComments = computed(() => {
  const clicked = comments.value.find((c) => c.id === openTextCommentId.value)
  if (!clicked) return []
  const { start, end } = clicked.anchor
  if (start == null || end == null) return [clicked]
  return comments.value.filter(
    (c) => c.anchor.kind === 'text' && c.anchor.start === start && c.anchor.end === end
  )
})

/**
 * Where to place the thread popover, in coordinates relative to the body.
 *
 * Read from the clicked element rather than tracked reactively: the mark is
 * re-created on every render of the body, so a stored reference would go stale.
 */
const textCommentPos = computed(() => {
  const el = textCommentAnchorEl.value
  const host = contentRef.value
  if (!el || !host) return null
  const r = el.getBoundingClientRect()
  const h = host.getBoundingClientRect()
  return { top: r.bottom - h.top + 6, left: Math.max(0, r.left - h.left) }
})

function handleCheckboxToggle(index: number) {
  const current = entry.value
  const view = viewData.value
  if (!current || !view) return
  // A checkbox click is a WRITE (it schedules a content PATCH to the row's
  // address). Checkboxes render inside markdown and cannot be hidden by a
  // `v-if` the way a button can, so the affordance gate lives at the handler.
  if (!canUpdate.value) {
    uiStore.warning('Update not permitted for this entity')
    return
  }
  if (accepting.value) {
    uiStore.warning('Applying a suggestion; try again in a moment')
    return
  }
  let newContent: string
  try {
    newContent = toggleCheckboxInSource(current.content || '', index)
  } catch (err) {
    // The toggler is intentionally narrower than the renderer (see
    // checkboxToggle.ts — `*`, `+`, ordered-list checkboxes parse as
    // task items in marked but are rejected here). Surface the thrown
    // detail so users know which line of the source the click missed.
    const detail = getErrorMessage(err, 'unknown error')
    uiStore.error(`Failed to toggle checkbox: ${detail}`)
    console.error(err)
    return
  }
  // Apply optimistically so a second click within the debounce window
  // toggles the post-first state (the source for the next toggle is
  // viewData.entry.content). Without this, two rapid clicks on the
  // same index would each toggle the unchanged server content and
  // net to zero on the next PATCH.
  const nextSections = view.sections.map((s) =>
    isEntryContentSection(s) ? { ...s, content: newContent } : s
  )
  viewData.value = { ...view, entry: { ...current, content: newContent }, sections: nextSections }
  contentAutoSave.scheduleContentSave(newContent)
}

// Keyboard shortcuts
function handleKeydown(e: KeyboardEvent) {
  if (isInputFocused()) return
  if (isAnyModalOpen()) return
  if (document.querySelector('.shortcuts-overlay')) return

  if (e.key === 'e' || e.key === 'E') {
    e.preventDefault()
    if (!canUpdate.value) {
      uiStore.warning('Edit not permitted for this entity')
      return
    }
    editEntity()
  }
  if ((e.key === 'Delete' || e.key === 'Backspace') && entry.value) {
    e.preventDefault()
    if (!canDelete.value) {
      uiStore.warning('Delete not permitted for this entity')
      return
    }
    void requestDelete()
  }
  if (e.key === 'p' && scopeNav.value?.prev) {
    e.preventDefault()
    navigateScope('prev')
  }
  if (e.key === 'n' && scopeNav.value?.next) {
    e.preventDefault()
    navigateScope('next')
  }
  if (e.key === 'Escape' && backTarget.value) {
    e.preventDefault()
    router.push(backTarget.value.to)
  }
}

function closeOverflow() {
  showOverflowMenu.value = false
}
watch(showOverflowMenu, (open) => {
  if (open) document.addEventListener('click', closeOverflow)
  else document.removeEventListener('click', closeOverflow)
})

// Commands — separate fetch, abortable to survive rapid navigation
// (BUG-6C3V: a stale fetch resolving against an unmounted component).
let commandsAbort: AbortController | null = null

async function loadCommands() {
  commandsAbort?.abort()
  commandsAbort = new AbortController()
  const localAbort = commandsAbort
  try {
    loadedCommands.value = await getCommands(
      { pageType: 'entity', entityType: props.entityType },
      localAbort.signal
    )
  } catch (err) {
    if (localAbort.signal.aborted) return
    if (isCancelledFetch(err)) return
    console.error('Failed to load commands:', err)
    loadedCommands.value = []
  }
}

function runCommand(cmd: Command) {
  commandModalRef.value?.runCommand(cmd)
}

// Lua actions offered on this page (TKT-VVS16W). The server publishes each
// as `_actions['action:<id>']` only when type, face, `when` and `permission`
// all hold, so the page renders exactly the keys it is given and decides
// nothing itself. Sorted by label so the order does not depend on map order.
interface DetailAction {
  id: string
  label: string
  config: ActionConfig
}
const detailActions = computed<DetailAction[]>(() => {
  const out: DetailAction[] = []
  for (const [key, allowed] of Object.entries(entry.value?._actions ?? {})) {
    if (!allowed || !key.startsWith('action:')) continue
    const id = key.slice('action:'.length)
    const config = schemaStore.getAction(id)
    if (config) out.push({ id, label: config.label || id, config })
  }
  return out.sort((a, b) => a.label.localeCompare(b.label))
})
const detailActionBusy = ref(false)
const { reportResult, reportError } = useActionFeedback()

async function runDetailAction(action: DetailAction, ev?: Event) {
  if (detailActionBusy.value) return
  const { id, label, config } = action
  const triggerEl = ev?.currentTarget instanceof HTMLElement ? ev.currentTarget : null
  if (config.confirm) {
    const ok = await confirm({
      title: `${label}?`,
      message:
        typeof config.confirm === 'string'
          ? config.confirm
          : `Run ${label} on this entity?`,
      confirmLabel: label,
    })
    if (!ok) return
  }
  // The served face address, never the bare id: the server checks the face
  // against `available_on.faces` and hands it to the script as entity.face.
  const address = servedRef.value
  detailActionBusy.value = true
  try {
    const res = await runAction(id, address)
    reportResult(res, `${label}: done`)
    if (res?.redirect) {
      router.push(res.redirect)
      return
    }
    // The script usually rewrote this entity; show the result without a
    // manual refresh.
    await loadView()
  } catch (err) {
    reportError(err, triggerEl, label)
  } finally {
    detailActionBusy.value = false
  }
}

async function loadView() {
  loading.value = true
  error.value = null
  // Report EVERY load to the global activity bar, including the ones where
  // previous content stays on screen.
  //
  // Holding the old entity is what stops the page collapsing, but it also
  // means a prev/next step looks like NOTHING happened until the new data
  // lands — the click reads as ignored. Stale content with no indicator is
  // the failure mode; the bar is what distinguishes "showing you the
  // previous one while the next loads" from "frozen".
  //
  // A bar is the right shape for that precisely because it does not
  // contradict the content: it is peripheral and additive, where a spinner
  // replacing the article would assert there is nothing to see.
  //
  // The route itself settles in ~99ms while this fetch runs ~2s, so without
  // reporting here the bar would never cover the part that actually takes
  // time. The 250ms gate still suppresses it entirely when the fetch is
  // quick.
  const settleRouteLoad = beginRouteLoad()
  try {
    // The world rides the request, so the entry resolves to that world's face
    // and every collection entity resolves through the same world, per
    // neighbour (TKT-WRLDAPI item 4b). Without it this page rendered draft
    // content while the selector said "published" — the API was correct and
    // the page simply never asked.
    viewData.value = await fetchView(props.entityType, props.entityId, worldParam.value)
    if (viewData.value?.entry) {
      // Seed the autosave baseline so the first toggle's no-op
      // suppression can compare against server state without waiting
      // for the response of a sentinel PATCH.
      contentAutoSave.recordServerSnapshot(viewData.value.entry)
    }
    // loadComments swallows its own failures (see its doc): a comment-service
    // problem must not fail the entity view it decorates.
    await Promise.all([loadCommands(), loadScopeNav(), loadComments()])
  } catch (err) {
    if (isCancelledFetch(err)) return
    error.value = getErrorMessage(err, 'Failed to load entity')
    console.error('Failed to load entity view:', err)
  } finally {
    loading.value = false
    settleRouteLoad?.()
  }
}

// Actions
function editEntity() {
  // The backend refuses to write through inaccessible (git-crypt encrypted)
  // entities; bail out client-side so the Edit shortcut is also a no-op.
  if (isInaccessible.value) return
  if (!editFormId.value) {
    uiStore.error('No edit form configured for this entity type')
    return
  }
  router.push({ name: 'form-edit', params: { id: editFormId.value, entityId: servedRef.value } })
}

async function requestDelete() {
  if (!entry.value) return
  // Addressed to the row on screen. On the bare face that is the entity; on
  // a non-bare face it is that face only (the server's rule for `ID@face`),
  // and the confirm says so — "delete" must not read as "unpublish" or the
  // reverse.
  const face = servedFace.value
  const ok = await confirm({
    title: face ? 'Delete Face?' : 'Delete Entity?',
    message: face
      ? `Are you sure you want to delete the ${faceLabel(face)} face of '${bareEntityId.value}'? ` +
        'Its other faces are kept. This action cannot be undone.'
      : `Are you sure you want to delete '${bareEntityId.value}'? This action cannot be undone.`,
    confirmLabel: 'Delete',
    danger: true,
    onConfirm: withConfirmError(
      async () => {
        // Use the entity API directly — entitiesStore.remove is the
        // canonical CRUD path; keep using it.
        const { useEntitiesStore } = await import('@/stores')
        const entitiesStore = useEntitiesStore()
        await entitiesStore.remove(props.entityType, servedRef.value)
      },
      'Failed to delete entity',
      uiStore
    ),
  })
  if (!ok) return
  uiStore.success('Entity deleted successfully')
  router.push(backTargetAfterDelete())
}

// --- Copy affordances (RULING 9) ---------------------------------------
//
// Offers ride the entity response as `_copies`, so there is nothing to fetch:
// they arrive with the view and refresh with it. CopyMenu renders only the
// allowed ones, as absent rather than disabled.
//
// The server's verdict, as-is. Offers are computed for the face it SERVED
// and the invoke names the source by id, so a promote offered on the bare
// face copies the bytes on screen. An earlier revision blanked the list on
// every world-bound page, which hid a correctly-offered promote on a chain
// hit on the bare face (atlas worlds issue 4).
//
// Always an array. The wire keeps `_copies` absent (no capability) distinct
// from `[]` (none declared), but this page renders nothing for both, so the
// distinction is collapsed HERE, once, rather than carried into every reader.
const copyOffers = computed<CopyOffer[]>(() => entry.value?._copies ?? [])

// Faces render on EVERY screen, world-bound or not — a reader wants the way
// back to the draft, an author wants to see what readers see, and the
// multilingual case has no privileged direction. Each entry is a plain link
// to the face's ADDRESS (`_faces[].ref`, e.g. `POL-1@published`), which the
// server serves literally under any world — so the menu never has to work
// out which declared world happens to lead with a face, and never offers a
// dead control for a face no world heads.
const faceOptions = computed<Face[] | undefined>(() => entry.value?._faces)

// The face's address, for an older server that sends no `ref`: the stored
// coordinate appended to the bare id is that face's spelling for every
// non-bare face; the bare face has only the bare id.
function faceAddress(f: Face): string {
  if (f.ref) return f.ref
  return f.face ? `${bareEntityId.value}@${f.face}` : bareEntityId.value
}

function goToFace(f: Face) {
  const ref = faceAddress(f)
  // The REST of the query is preserved — `from`/`scope` keep the back button
  // and prev/next working, and the world stays what it was: an explicit
  // address is literal under any world, so switching face never has to switch
  // world.
  const query = { ...route.query }
  if (!ref.includes('@')) {
    // A bare address is literal only in the default world (under any other,
    // the world resolves it). That is the ONE case where the face switch has
    // to name the world — spelled `default` when a configured default would
    // otherwise apply, dropped when it would not, exactly as setWorld does.
    if (schemaStore.defaultWorld) query.world = DEFAULT_WORLD
    else delete query.world
  }
  router.push({ path: `/entity/${props.entityType}/${ref}`, query })
}

const copyBusy = ref(false)

async function runCopy(offer: CopyOffer) {
  // Every offer is same-entity (the server filters cross-entity definitions
  // out), so the invoke names the definition and the source and nothing else.
  //
  // Capture the subject. `copyBusy` only disables THIS menu's button — the
  // scope-nav shortcuts (P/N), the back button and the sidebar stay live, so
  // the page can be showing a different entity by the time the invoke
  // resolves. Without this the success toast names a face on the entity the
  // user has already left, and `loadView()` fires a second, pointless fetch
  // for the one they are now on. Same shape as `pinEntityForFlush` below.
  const subject = bareEntityId.value
  const label = offer.label || offer.name
  copyBusy.value = true
  try {
    const res = await invokeCopy(offer.name, subject)
    // The toast is the operator's `on_success.message`, or the copy's own
    // label — the button the reader just pressed — never rela's "Created the
    // X face" (TKT-5SZG2L). `{face}` here is the face WRITTEN, which
    // `res.face` reports as a stored coordinate.
    const message =
      worldText(offer.onSuccess?.message, {
        ...textVars.value,
        face: faceLabel(res.face),
      }) || label
    if (bareEntityId.value !== subject) {
      // Still worth telling them it succeeded — they asked for it — but name
      // the entity, since the page in front of them is no longer the subject.
      uiStore.success(`${message} (${subject})`)
      return
    }
    uiStore.success(message)
    await landAfterCopy(offer, subject, res.face)
  } catch (err) {
    // The kernel re-authorizes, so a 403 here is not a contradiction of
    // `allowed` — it is the boundary doing its job on a hint that went stale
    // (a permission revoked between render and click). Report it plainly, and
    // name the subject if the user has since navigated away — an unqualified
    // "Copy failed" beside an unrelated entity reads as that entity failing.
    const detail = getErrorMessage(err, 'Copy failed')
    uiStore.error(bareEntityId.value === subject ? detail : `${subject}: ${detail}`)
  } finally {
    copyBusy.value = false
  }
}

// landAfterCopy navigates per the copy's `on_success.landing`. The default is
// the face WRITTEN (an editor who adopts a policy usually wants to see the
// adopted text), addressed as `ID@face` so it is literal under any world; a
// copy into the bare face reloads in place, since the bare address would
// re-resolve. `stay` reloads in place; a world lands the bare id in that
// world; a face lands on that face's address. Either way the offers
// recompute, since a face that now exists may no longer be offered.
//
// The server validates a landing at load, so a `world`/`face` arm with no
// name is not a state this deployment produces — but a response is data,
// and the guard is what keeps a hand-crafted or older one from navigating
// to a literal `ID@undefined`. A mode this build does not know reloads in
// place rather than passing for `written`: the landing was a declaration,
// and "written" would be this code inventing one.
async function landAfterCopy(offer: CopyOffer, subject: string, writtenFace: string) {
  const landing = offer.onSuccess?.landing
  const path = `/entity/${props.entityType}/${subject}`
  const toFace = (face: string) =>
    router.push({ path: `${path}@${face}`, query: { ...route.query } })
  switch (landing?.mode ?? 'written') {
    case 'written':
      if (writtenFace) toFace(writtenFace)
      else await loadView()
      return
    case 'stay':
      await loadView()
      return
    case 'world':
      if (landing?.world) {
        router.push({
          path,
          query: worldQuery(landing.world, route.query, schemaStore.defaultWorld),
        })
      } else {
        await loadView()
      }
      return
    case 'face':
      if (landing?.face) toFace(landing.face)
      else await loadView()
      return
    default:
      await loadView()
  }
}

// The operator's announcement for the world on screen, or '' to announce
// nothing. Config, not data — served identically to every principal.
const worldBanner = computed<string>(
  () => (world.value ? schemaStore.worlds.get(world.value)?.banner : '') || ''
)

// --- Operator chrome text ------------------------------------------------
//
// The page has NO sentence of its own about worlds or faces. Every one it
// used to have ("Read-only in this world — edit from Concept", "This entity
// has no published face yet", "Created the published face") was rela's
// storage vocabulary shown to a reader who never chose those words. What an
// operator declares in schema.yaml renders verbatim (placeholders
// substituted); what they do not declare renders nothing (TKT-5SZG2L).

// The declared label of a face, by its DECLARED name (the vocabulary the
// address and `_world.face` use). '' when the type names none.
function faceLabelOf(declared: string): string {
  const faces = typeDef.value?.faces
  if (!faces) return declared
  return faces[declared]?.label || declared
}

// A face's display label by its coordinate, for the copy result, which
// reports what it wrote.
function faceLabel(stored: string): string {
  return schemaStore.faceLabel(props.entityType, stored) || stored
}

const textVars = computed<WorldTextVars>(() => ({
  face: faceLabelOf(servedFace.value),
  world: world.value,
  title: entryTitle.value,
}))

const worldInfo = computed(() => (world.value ? schemaStore.worlds.get(world.value) : undefined))

// The note for a NON-BARE face the principal may not write, in the
// operator's words for THAT face (`faces.<name>.messages.read_only`). A bare
// face without `update` is an ordinary permission denial; a non-bare face
// WITH `update` (a translator on `nl`) is simply editable; neither gets a
// note. No button either: the face menu already reaches the bare face.
const readOnlyNote = computed<string>(() => {
  if (worldAbsent.value || !servedFace.value || mayUpdate.value) return ''
  const declared = typeDef.value?.faces?.[servedFace.value]
  return worldText(declared?.messages?.read_only, textVars.value)
})

// The note about the DOCUMENT on screen, in the operator's words for its
// face (`faces.<name>.messages.notice`) — "this is a draft, not in force".
//
// Deliberately NOT gated on `mayUpdate`, which is the ONE thing separating it
// from readOnlyNote above: a draft is not in force for the editor rewriting it
// either, and a face writable by definition could never satisfy that guard.
// That is the whole reason this is a second key rather than a relaxed first.
//
// `!servedFace` means the type declares no faces at all (BUG-HC6I2T removed
// `bare_face`, so a faced type stores no bare row and always serves a named
// face). No face on screen, nothing for a per-face note to be about.
//
// The `worldAbsent` term is BELT-AND-BRACES, not the mechanism: the banner's
// own `v-if` already gates on `!worldAbsent`, so removing this term changes
// nothing today. It is kept — as readOnlyNote keeps its copy — so a future
// caller rendering this note outside that banner inherits the silence rather
// than announcing a face the world never served.
const noticeNote = computed<string>(() => {
  if (worldAbsent.value || !servedFace.value) return ''
  const declared = typeDef.value?.faces?.[servedFace.value]
  return worldText(declared?.messages?.notice, textVars.value)
})

// The face notes the banner shows, in order, undeclared ones dropped.
//
// Ordered widest-scope-first: the DOCUMENT's own status, then the qualifier
// on THIS reader's permission. Keeping that as one array literal is the point
// — order is policy, and the template should not be where it is decided (nor
// re-litigated when a third key arrives).
const bannerNotes = computed<string[]>(() => [noticeNote.value, readOnlyNote.value].filter(Boolean))

// The note for an entity with no face in this world, in the world's words.
const absentNote = computed<string>(() => {
  if (!worldAbsent.value) return ''
  return worldText(worldInfo.value?.messages?.absent, textVars.value)
})

// `on_absent.redirect`: the operator would rather send the reader somewhere
// than explain an absence. Fires on every VIEW the page loads, not on the
// absent flag: scope-nav from one absent entity to the next keeps the flag
// true throughout, and a watcher on it would fire for the first entity
// only. Fires on the schema too, since `worldInfo` is empty until the store
// has loaded and a redirect must not depend on which fetch won. The target
// is a declared world, spelled through setWorld so `default` becomes the
// right query for this deployment.
//
// The loader refuses a redirect chain that returns to a visited world, so a
// loop is not a state this deployment produces. The set below is the
// client's own guard for a schema it did not validate: every world this
// page has redirected from or to, for this entity, is never a target again.
// A loop therefore stops after one hop, a chain `a → b → c` still runs, and
// a schema reload cannot push the same hop twice.
const redirectVisited = new Set<string>()
watch(
  () => props.entityId,
  () => redirectVisited.clear()
)
watch([viewData, worldInfo], ([, info]) => {
  if (!worldAbsent.value) return
  const target = info?.on_absent?.redirect
  if (!target) return
  const norm = (w: string) => (w === DEFAULT_WORLD ? '' : w)
  if (redirectVisited.has(norm(target))) return
  redirectVisited.add(norm(world.value))
  redirectVisited.add(norm(target))
  setWorld(norm(target))
})

// --- The header's mobile home ------------------------------------------
//
// Every header affordance that is not Edit or Delete lives in the mobile
// overflow menu, and these computeds are what make that checkable in one
// place. Three features (FaceMenu, CopyMenu, History) each shipped to the
// desktop row alone, so at phone width they vanished entirely — not degraded
// layout, lost functionality: no way to publish a policy or switch language.

// History is a per-DEPLOYMENT capability, not a permission. Postgres has
// version history; fs and mem do not, and their `/_history` answers a named
// 501. Rendering the button regardless is the affordance-that-lies shape, so
// it is ABSENT when the capability is — never disabled, which would still
// advertise a feature this deployment cannot provide.
const showHistory = computed(() => schemaStore.historyEnabled)

// The same filters the menu components apply to their own props, so the
// mobile rows and the desktop menus offer an identical set. Duplicating the
// PREDICATE would let the two drift; duplicating the call does not.
const overflowFaces = computed<Face[]>(() => faceOptions.value ?? [])
const overflowCopies = computed<CopyOffer[]>(() => copyOffers.value.filter((o) => o.allowed))

// Whether the overflow button renders at all. One expression, read by both
// the button and its contents: a fourth affordance is added here and cannot
// then be reachable on desktop only.
const hasOverflow = computed(
  () =>
    commands.value.length > 0 ||
    detailActions.value.length > 0 ||
    overflowFaces.value.length > 0 ||
    overflowCopies.value.length > 0 ||
    canDuplicate.value ||
    showHistory.value
)

function backTargetAfterDelete(): string {
  if (backTarget.value) return backTarget.value.to
  const listId = schemaStore.findListIdForEntityType(props.entityType)
  if (listId) return `/list/${listId}`
  return '/'
}

// Section navigation helpers
//
// entityTarget is the single source of truth for a section entry's destination,
// bound to the RouterLinks below so a cmd/middle-clicked tab lands exactly where
// a plain click does.
//
// The WORLD rides along, for the same reason historyTarget carries it (BUG-2):
// a neighbour followed from a world-bound page must resolve in that world, or
// the reader silently lands on the DEFAULT face of an entity whose row they
// just saw badged as a fallback (TKT-6NCSSC).
//
// A cellLink is left alone: it is a server-resolved per-column target that may
// point outside the entity routes entirely, so appending a world to it would
// be inventing a parameter its destination never declared.
//
// Nil: returns undefined when the entry has no resolvable route (empty type),
// and the template renders plain text instead of an anchor.
function entityTarget(
  entity: { id: string; type: string },
  cellLink?: string
): RouteLocationRaw | undefined {
  const path = entityDetailHref(entity, { cellLink })
  if (!path) return undefined
  if (cellLink) return path
  return worldParam.value ? { path, query: { world: worldParam.value } } : path
}

function navigateToEntity(entity: { id: string; type: string }, cellLink?: string) {
  const target = entityTarget(entity, cellLink)
  if (!target) return
  router.push(target)
}

// Click handler for the section-table cell anchors. Those are real <a href>
// elements, so a modifier or middle click must reach the BROWSER — it is what
// opens the new tab. Only a plain left-click is ours to intercept, and only
// then do we preventDefault and route in-SPA. An unconditional `.prevent` here
// suppressed cmd/ctrl/shift/middle clicks too, which was this ticket's own bug.
function onCellLinkClick(
  event: MouseEvent,
  entity: { id: string; type: string },
  cellLink?: string
) {
  if (shouldDeferToBrowser(event)) return
  event.preventDefault()
  navigateToEntity(entity, cellLink)
}

// A row's form opens on the row's ADDRESS: under a world a collection row is a
// neighbour's RESOLVED face, and its bare id would edit a state the page is
// not showing.
function navigateToEdit(formId: string, row: { id: string; _self?: string }) {
  router.push({ name: 'form-edit', params: { id: formId, entityId: entityRef(row) } })
}

// Look up a schema PropertyDef for an entity type's property. Returns
// undefined when the entity type or property isn't in the schema. Used
// to pre-resolve defs at section level (RR-UD1H) instead of in every
// cell render.
function getPropertyDef(entityType: string, propertyName: string): PropertyDef | undefined {
  const et = schemaStore.getEntityType(entityType)
  return et?.properties?.[propertyName]
}

// entryDisplayValue: the entry-section analogue of `rowDisplayValue` below —
// prefer the live `entry.properties` over the display-stringified
// `fields[i].values` server mirror, which `handlePropertyApplied` never
// updates (it rewrites `entry.properties` only).
//
// Before TKT-HOIX1 the entry display path was reachable only when the ACL
// denied EVERY field in the section, so nothing could edit those values and
// the mirror could not go stale. With `render` defaulting to display, a
// display-rendered section is now the common case and can sit alongside a
// `render: input` section editing the same property — at which point the
// display section would show last-loadView's string forever. Same bug class
// as RR-FC1C, same fix.
function entryDisplayValue(field: ViewSectionField): unknown {
  const props = entry.value?.properties
  if (field.property && props && field.property in props) {
    return props[field.property]
  }
  return field.values ?? []
}

function mapFieldsToProperties(fields: ViewSectionField[] | undefined): PropertyItem[] {
  if (!fields) return []
  // Pre-resolve PropertyDef for the entry entity once (RR-UD1H). For
  // entry-level properties the entity type is fixed for the section.
  // When the property isn't in the schema we leave propertyDef undefined
  // and let PropertyDisplay fall back to a WidgetRoutingHint
  // (RR-UD2B) -- no more synthesised PropertyDef lies.
  const entryType = entry.value?.type
  return fields.map((field) => {
    // PropertyDisplay's `name` is used as a vue list key; favor the raw
    // property name when available and fall back to a slugged label so
    // older shapes still render.
    const name = field.property ?? field.label.toLowerCase().replace(/\s+/g, '_')
    const def = entryType && field.property ? getPropertyDef(entryType, field.property) : undefined
    return {
      name,
      label: field.label,
      value: entryDisplayValue(field),
      propType: field.propType,
      propertyDef: def,
      span: field.span,
      inaccessible: field.inaccessible ?? false,
      inaccessibleReason: field.property ? inaccessibleByName.value.get(field.property) : undefined,
      attachments: field.property ? entry.value?._attachments?.[field.property] : undefined,
      max: def?.max,
    }
  })
}

// FieldRow bundles per-field data for cards/list rendering. Computed
// once per (entity, field) instead of recomputed inline on every
// reactive tick (RR-UD2A).
interface FieldRow {
  field: ViewSectionField
  widget: Component
  hint: WidgetRoutingHint
}

// One resolved cell of a nested row: the widget to render plus the raw value
// that widget wants. `property` is only a v-for key.
interface NestedCell {
  property: string
  propertyName: string
  widget: Component
  value: unknown
}

// fieldRowsFor returns the precomputed FieldRow array for one entity's
// fields. Cards/list templates iterate this instead of calling helper
// functions inline per cell.
function fieldRowsFor(ent: { fields?: ViewSectionField[] }): FieldRow[] {
  return (ent.fields ?? []).map((field) => {
    const hint = viewFieldRoutingHint(field)
    return {
      field,
      hint,
      widget: defaultRegistry.resolveFromHint(hint),
    }
  })
}

// Memoize per (section reference, entry reference) so the SectionEditForm's
// `watch(() => props.fields)` only fires when the underlying section or
// entry identity changes — not on every reactive tick (RR-FB2D NEW-4).
const sectionEditFieldsCache = new WeakMap<
  ViewSection,
  { entry: Entity; fields: SectionEditField[] }
>()
function memoBuildSectionEditFields(section: ViewSection, ent: Entity): SectionEditField[] {
  const cached = sectionEditFieldsCache.get(section)
  if (cached && cached.entry === ent) return cached.fields
  const fields = buildSectionEditFieldsPure(section.fields, ent, getPropertyDef)
  sectionEditFieldsCache.set(section, { entry: ent, fields })
  return fields
}

function sectionShouldRouteToInlineEdit(section: ViewSection, ent: Entity): boolean {
  // Writability is per field (`_fields`), computed by the server for the
  // face on screen; the inline form writes to that face's address.
  return sectionShouldRouteToInlineEditPure(section.fields, ent, getPropertyDef)
}

// The properties inline-edit section renders its OWN heading row (so the
// heading and the auto-save indicator are flex siblings on one line —
// TKT-U62DVR). For that case the generic <h2> above is suppressed to avoid
// a duplicate heading. Every other section keeps the shared <h2>.
//
// Requires a truthy `section.heading`: SectionEditForm only renders its
// header row when `heading` is non-empty (v-if="heading"), so if we
// suppressed the generic <h2> for a headingless properties section we'd
// end up with NO heading at all and a bare indicator. Gating on the same
// truthiness keeps the two guards in agreement (RR-32ARO9).
// True when the shared <h2> above actually renders, i.e. when this section can
// be named by that heading element via aria-labelledby. When it does not render
// (a properties section drawing its own heading row, or a headingless section)
// the section falls back to aria-label from the heading text, so a section is
// never left as an unnamed region.
function sectionRendersGenericHeading(section: ViewSection): boolean {
  if (sectionRendersOwnHeading(section)) return false
  return !!section.heading || (section === entryContentSection.value && !!checkboxStats.value)
}

function sectionRendersOwnHeading(section: ViewSection): boolean {
  const ent = entry.value
  return (
    !!section.heading &&
    section.display === 'properties' &&
    ent != null &&
    sectionShouldRouteToInlineEdit(section, ent)
  )
}

// One-shot dedupe for the 401/403 → loadView path. Cleared on each
// loadView call to allow the next 4xx to refetch again.
let pendingRefetch = false

function handlePropertyApplied(
  prop: string,
  value: unknown,
  applyOwner: { type: string; id: string }
) {
  const view = viewData.value
  const nextEntry = applyPropertyToEntry(view?.entry ?? null, prop, value, applyOwner)
  if (!nextEntry || !view) return
  viewData.value = { ...view, entry: nextEntry }
}

function handleSectionEditError(msg: string, info?: AutoSaveErrorInfo) {
  uiStore.error(msg)
  if ((info?.status === 401 || info?.status === 403) && !pendingRefetch) {
    pendingRefetch = true
    void loadView().finally(() => {
      pendingRefetch = false
    })
  }
}

function handleVerdictFlip(_prop: string, label: string) {
  uiStore.warning(`Permission changed — your unsaved edit to '${label}' was discarded`)
}

// ─── Per-row inline edit on cards/list sections (TKT-IHC7C) ──────────────

// Above this row count, a cards/list section falls back to display mode
// for ALL rows. SectionEditForm instances are cheap individually but
// quadratic in aggregate (each holds its own debounce timers and watch);
// 100 rows is the soft cap (RR-FC1D). Above this the user is more
// likely browsing than editing.
const INLINE_EDIT_ROW_CAP = 100

// Thin SFC adapter — the cap-behaviour logic lives in the pure module
// so it's unit-testable without mounting EntityDetail.
function rowShouldRouteToInlineEdit(ent: ViewEntity, rowCount: number): boolean {
  // Per-field verdicts from the server, as for the entry. Under a world each
  // row is a NEIGHBOUR's resolved face, and its inline form writes to the
  // row's own address (`_self`), so the edit lands on the face on screen.
  return rowShouldRouteToInlineEditPure(ent, rowCount, INLINE_EDIT_ROW_CAP, getPropertyDef)
}

// Memoize per-row field shape, keyed on the row reference. Spread-clone
// of the row in handleRowPropertyApplied produces a new reference for
// THAT row only — surrounding rows retain their references and their
// cached entry. Cache invalidation is naturally GC-driven via the
// WeakMap (RR-FC1E NEW-3).
const rowEditFieldsCache = new WeakMap<ViewEntity, SectionEditField[]>()
function memoBuildRowEditFields(ent: ViewEntity): SectionEditField[] {
  const cached = rowEditFieldsCache.get(ent)
  if (cached) return cached
  const fields = buildSectionEditFieldsPure(ent.fields, ent, getPropertyDef)
  rowEditFieldsCache.set(ent, fields)
  return fields
}

// rowDisplayValue: per-cell display-mode read prefers `_props` over the
// display-stringified `fields[i].values`. This eliminates the
// stale-string-mirror bug where a verdict flips writable→display and
// the cell shows last-loadView's display string instead of the
// post-edit value (RR-FC1C).
function rowDisplayValue(ent: ViewEntity, field: ViewSectionField): unknown {
  if (field.property && ent._props && field.property in ent._props) {
    return ent._props[field.property]
  }
  return field.values ?? []
}

// nestedCellsFor resolves one nested row's configured `columns:` into widget
// renders, so a nested section shows dates, enums and booleans the way every
// other surface does instead of the raw stored string.
//
// Two deliberate choices:
//
// It reads the RAW value from `_props` rather than the cell's pre-stringified
// `values`, because the widget is the thing that formats: handing DateWidget
// `"2026-08-12 00:00:00 +0000 UTC"` would render that verbatim.
//
// It routes on the cell's server-resolved `widget` name, NOT through
// viewFieldRoutingHint. That helper maps any truthy propType to 'enum-list' to
// preserve the cards/list Badge behaviour, which would badge a date. The
// server already resolved the right widget per property (`resolveWidget`), so
// asking the registry for it by name keeps one type→widget decision instead of
// a second heuristic that disagrees with it.
//
// Columns come from the NODE, not the section: a nested section has no single
// column list, because `parent_columns` and `child_columns` are each keyed by
// entity type, so a row's columns depend on its level and its type.
//
// Relation columns have no property and no relation widget, so they keep the
// server's joined string via the text fallback — the same fall-through
// EntityList documents.
function nestedCellsFor(node: ViewTreeNode): NestedCell[] {
  const out: NestedCell[] = []
  for (const [idx, col] of (node.columns ?? []).entries()) {
    const cell = node.cells?.[idx]
    const values = cell?.values ?? []
    const raw =
      col.property && node.entity._props && col.property in node.entity._props
        ? node.entity._props[col.property]
        : values
    // Empty renders nothing on a dense row — the same contract as a blank
    // table cell, so a sparsely-set column does not become a row of dashes
    // (see isDenseEmpty).
    if (isDenseEmpty(raw)) continue

    out.push({
      property: col.property ?? col.relation ?? String(idx),
      propertyName: col.property ?? '',
      widget: defaultRegistry.resolve(cell?.widget, undefined),
      value: raw,
    })
  }
  return out
}

// Owner → (sectionIdx, rowIdx) index, rebuilt per viewData change so
// `handleRowPropertyApplied` is O(1) instead of O(sections × rows)
// (RR-FC1E NEW-3). The rebuild is itself O(rows) but only fires when
// `viewData.value` reference changes — including after every confirmed
// row PATCH, which is acceptable.
const rowIndex = ref(new Map<string, { sectionIdx: number; rowIdx: number }>())
watch(
  viewData,
  (vd) => {
    const next = new Map<string, { sectionIdx: number; rowIdx: number }>()
    if (vd?.sections) {
      vd.sections.forEach((section, sectionIdx) => {
        section.entities?.forEach((ent, rowIdx) => {
          next.set(`${ent.type}/${ent.id}`, { sectionIdx, rowIdx })
        })
      })
    }
    rowIndex.value = next
  },
  { immediate: true }
)

function handleRowPropertyApplied(
  prop: string,
  value: unknown,
  applyOwner: { type: string; id: string }
) {
  const view = viewData.value
  if (!view) return
  const key = `${applyOwner.type}/${applyOwner.id}`
  const loc = rowIndex.value.get(key)
  if (!loc) return // row deleted / repositioned beyond identity reach
  const section = view.sections[loc.sectionIdx]
  const currentRow = section?.entities?.[loc.rowIdx]
  if (!currentRow) return
  // applyPropertyToRow rejects the apply if the row at this location
  // no longer matches the owner identity (defensive against an index
  // rebuilt mid-flight against a different layout).
  const nextRow = applyPropertyToRow(currentRow, prop, value, applyOwner)
  if (!nextRow) return
  const nextSections = view.sections.map((s, i) => {
    if (i !== loc.sectionIdx) return s
    return {
      ...s,
      entities: s.entities?.map((e, j) => (j === loc.rowIdx ? nextRow : e)),
    }
  })
  viewData.value = { ...view, sections: nextSections }
}

function shouldUseBadge(value: string, propType?: string): boolean {
  return !!propType && !!value
}

function scrollToSection(sectionId: string) {
  const el = document.getElementById(sectionId)
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

// Lifecycle
onMounted(() => {
  document.addEventListener('keydown', handleKeydown)
  loadView()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleKeydown)
  commandsAbort?.abort()
  // Flush any pending toggle PATCH so navigating away mid-debounce
  // doesn't silently drop the user's click. commitImmediately returns
  // a promise; we don't await it because Vue's onBeforeUnmount is
  // synchronous — the FIFO chain will run to completion regardless.
  void contentAutoSave.commitImmediately()
})

onUnmounted(() => document.removeEventListener('click', closeOverflow))

// Switching WORLD reloads the view, but is deliberately NOT folded into the
// entity watcher below.
//
// That watcher flushes pending autosaves against the PREVIOUS entity's
// identity before loading the next one. A world change is the SAME entity seen
// through a different world, so there is no previous identity to pin and no
// cross-entity flush to arrange — running that machinery would capture the
// current entity as its own "previous" and commit a flush nobody asked for.
//
// What a world change does need is a refetch, because the face being displayed
// changes even though the id does not.
watch(
  () => worldParam.value,
  () => {
    loadView()
  }
)

// Watch for route changes
watch(
  () => [props.entityType, props.entityId],
  (_next, prev) => {
    // Flush pending toggles for the previous entity before loading the
    // next one. The composable reads entity identity from
    // getEntityType/getEntityId at fire time, which would resolve to
    // the new entity by the time the FIFO chain runs — so we capture
    // the previous identity for the duration of the flush via a
    // one-shot override before triggering commit.
    const [prevType, prevId] = prev
    const fireWith = { type: prevType, id: prevId }
    pinEntityForFlush.value = fireWith
    void contentAutoSave.commitImmediately().finally(() => {
      if (pinEntityForFlush.value === fireWith) pinEntityForFlush.value = null
    })
    loadView()
  }
)
// ---------------------------------------------------------------------------
// Create related entity (TKT-R4BMJM)
// ---------------------------------------------------------------------------

/** The affordance + target a create modal is currently open for. */
const activeCreate = ref<{
  create: ViewSectionCreate
  target: ViewSectionCreateTarget
} | null>(null)

/**
 * Opens the create flow for one target.
 *
 * `flow` decides where it happens, and the two paths differ in how the pre-link
 * reaches the form — deliberately, not incidentally:
 *
 *   - PAGE navigates with query params. `DynamicForm` already parses them.
 *   - MODAL passes props. An embedded form reads an EMPTY query on purpose
 *     (DynamicForm.vue), because it mounts over whatever page the host is on and
 *     honouring that page's query would pre-fill the new entity from the host's
 *     params. Reusing the URL channel here would reintroduce exactly that bug.
 *
 * The world is carried on BOTH paths. It decides which face the new entity
 * lands in (`worlds.<name>.create`), and a faced type has no default row to fall
 * back to, so dropping it is a refusal rather than a silent default.
 */
function startCreate(create: ViewSectionCreate, target: ViewSectionCreateTarget) {
  if (create.flow === 'page') {
    router.push({
      path: `/form/${target.formId}`,
      query: buildCreateLinkQuery({
        relation: create.relation,
        peer: create.peerId,
        linkAs: create.linkAs,
        // Back to the entity the user started from, so the new item is visible
        // in the section that offered the button.
        returnTo: route.fullPath,
        template: target.template,
        world: worldParam.value || undefined,
      }),
    })
    return
  }
  activeCreate.value = { create, target }
}

function closeCreate() {
  activeCreate.value = null
}

/**
 * After a modal create: refresh the page.
 *
 * It does NOT link. The pre-link travels to the embedded form as a prop, and
 * that form owns BOTH directions — `linkAs: 'to'` rides its create payload,
 * `linkAs: 'from'` is its own post-create call. Linking here as well created the
 * same edge twice: the second attempt failed with "relation already exists" and
 * reported a link failure for an edge that was already correct.
 *
 * One implementation, two entry points, is the whole reason the pre-link is a
 * prop rather than a second code path — so the host must not re-implement half
 * of it.
 */
async function onCreated(created: Entity) {
  const active = activeCreate.value
  activeCreate.value = null
  if (!active) return

  // A whole-view refetch: there is no per-section fetch, so "in place" means
  // "no navigation", not a partial update.
  await loadView()

  // `create` implies no `read` (internal/acl/policy.go): a principal may create
  // a type they cannot read back, in which case the refetch legitimately does
  // not show the new row. Naming the id is the honest answer — silence would
  // look like the create failed, and an error would claim something false.
  //
  // A section's rows live in one of THREE places depending on its display mode
  // — `entities` (cards/list), `rows` (table), `tree` (nested) — so scanning
  // only `entities` reported "missing" for every table and nested section and
  // fired the toast even when the row was plainly on screen.
  if (!viewData.value?.sections.some((sec) => sectionContainsEntity(sec, created.id))) {
    uiStore.success(`Created ${created.id}.`)
  }
}

/** Does a rendered section carry this entity, in whichever shape it uses? */
function sectionContainsEntity(sec: ViewSection, id: string): boolean {
  if (sec.entities?.some((e) => e.id === id)) return true
  if (sec.rows?.some((r) => r.entityId === id)) return true
  return treeContainsEntity(sec.tree, id)
}

function treeContainsEntity(nodes: ViewTreeNode[] | undefined, id: string): boolean {
  return (nodes ?? []).some((n) => n.entity?.id === id || treeContainsEntity(n.children, id))
}
</script>

<template>
  <div class="entity-detail" :data-testid="`page-state-${pageState}`">
    <!-- Deliberately empty while loading below the threshold: no spinner,
         no reserved block, no layout spring. The ActivityBar carries the
         navigation case; this only paints for a slow cold load. -->
    <div v-if="showBlockLoader" class="loading-state">
      <div class="spinner" />
      <span>Loading…</span>
    </div>
    <div v-else-if="loading && !entry" class="entity-detail-placeholder" />

    <div v-else-if="error" class="error-state">
      <h2>Error</h2>
      <p>{{ error }}</p>
      <button class="btn btn-primary" @click="loadView">Retry</button>
    </div>

    <template v-else-if="entry">
      <!-- Back affordance + optional scope (prev/next) navigation. The bar
           renders when either a back target exists (?return_to or ?from)
           or the user is in a list-scoped context (?from drives scopeNav).
           Both can be present simultaneously. -->
      <div v-if="backTarget || scopeNav" class="scope-nav mobile-topbar">
        <BackButton v-if="backTarget" :target="backTarget" />
        <template v-if="scopeNav">
          <RouterLink v-if="scopeTarget('prev')" class="scope-nav-btn" :to="scopeTarget('prev')!">
            ← Prev <kbd>P</kbd>
          </RouterLink>
          <span v-else class="scope-nav-btn disabled">← Prev</span>
          <span class="scope-nav-progress">[{{ scopeNav.current }}/{{ scopeNav.total }}]</span>
          <span class="scope-nav-label">{{ scopeNav.label }}</span>
          <RouterLink v-if="scopeTarget('next')" class="scope-nav-btn" :to="scopeTarget('next')!">
            Next → <kbd>N</kbd>
          </RouterLink>
          <span v-else class="scope-nav-btn disabled">Next →</span>
        </template>
      </div>

      <!--
        The world banner, mirroring EntityList's. It says which world is being
        shown, that the page is read-only here, and offers the way back to the
        default world — which is where every write lands, since the API refuses
        `?world=` on a write.

        "Go to draft" is a plain navigation to the same id with no `?world=`,
        shown only when this principal may read the default world — a global
        role-level grant already reported per world by `/_schema`.worlds, so
        the check costs no extra request. It does NOT predict whether the
        specific ENTITY is readable there; that is the row gate's job and it
        answers on arrival, exactly as it would for a typed URL.

        There is deliberately no WorldBadge on the ENTRY: `_views` attaches
        `_world` to collection entities only (sections.go is the single call
        site), so a badge here would have nothing to render. The entry's own
        provenance is available on the entity GET, but this page reads the view
        — wiring a second request for a badge is not worth it. Collection
        entities, where the fallback-vs-real distinction actually bites, carry
        the badge below.
      -->
      <!--
        No face in the requested world: the page shows the BARE face, which is
        writable and where a promote starts from, so this banner must not read
        as read-only. It carries the world's announcement and the operator's
        `messages.absent` and nothing else — no rela sentence, no button (the
        face menu reaches every other face by address). With neither declared
        it does not render at all.
      -->
      <WorldBanner
        v-if="worldAbsent && (worldBanner || absentNote)"
        variant="absent"
        :label="worldBanner"
      >
        {{ absentNote }}
      </WorldBanner>

      <!--
        The ANNOUNCEMENT is operator config: `banner:` on the world in
        schema.yaml. The NOTES are the operator's `faces.<name>.messages.*`
        for the face on screen: `notice` about the DOCUMENT (a draft is not in
        force, whoever is reading), then `read_only` about the READER (a
        non-bare face they may not write). Nothing declared: nothing renders —
        the world does not make a page read-only, only a grant does, and a
        denial looks like any other denial.

        One span per note, not one interpolation: they are separate sentences
        from separate keys and must not run together as one line. Which notes
        and in what order is `bannerNotes`, not this template.
      -->
      <WorldBanner
        v-if="!worldAbsent && ((isWorldBound && worldBanner) || bannerNotes.length)"
        :label="isWorldBound ? worldBanner : ''"
      >
        <span v-for="(note, i) in bannerNotes" :key="i" class="banner-note">{{ note }}</span>
      </WorldBanner>

      <header class="detail-header">
        <div class="header-info">
          <span class="entity-type-badge">{{ typeDef?.label || entityType }}</span>
          <h1 class="text-wrap-anywhere">{{ entryTitle }}</h1>
        </div>
        <!-- Desktop actions -->
        <div v-if="!props.hideActions" class="header-actions desktop-actions">
          <button
            v-for="cmd in commands"
            :key="cmd.id"
            class="btn btn-command"
            @click="runCommand(cmd)"
          >
            {{ cmd.label }}
          </button>
          <button
            v-for="a in detailActions"
            :key="`action-${a.id}`"
            class="btn btn-command"
            :disabled="detailActionBusy"
            @click="runDetailAction(a, $event)"
          >
            {{ a.label }}
          </button>
          <FaceMenu :faces="faceOptions" @select="goToFace" />
          <!--
            Copy affordances (RULING 9). Renders nothing when no offer is
            allowed — a denied copy is ABSENT, not disabled — and nothing under
            a world, where copyOffers is empty; see its comment for why.
          -->
          <CopyMenu :offers="copyOffers" :busy="copyBusy" @invoke="runCopy" />
          <!--
            The page-header create menu: sections that opted into
            `create.in: [header]`. The server assembles and dedupes it, so this
            renders what it is given. Absent entirely when no section opted in,
            which is every view that predates TKT-R4BMJM.
          -->
          <SectionCreateButton
            v-for="hc in viewData?.create ?? []"
            :key="hc.relation"
            :create="hc"
            :menu-label="hc.heading || 'New'"
            @select="startCreate"
          />
          <RouterLink v-if="editTarget" class="btn btn-secondary" :to="editTarget">
            Edit <kbd>E</kbd>
          </RouterLink>
          <!--
            Gated on a DEPLOYMENT capability, not a permission: version history
            is postgres-only, and on fs/mem `/_history` answers a named 501. An
            ungated link could therefore only fail. Absent, not disabled — a
            greyed-out control still advertises a feature this deployment does
            not have. The mobile block below gates on the SAME flag; both sites
            must, which is the trap this header just walked into three times.
          -->
          <RouterLink v-if="showHistory" class="btn btn-secondary" :to="historyTarget"
            >History</RouterLink
          >
          <ExportMenu :url-for="(t: string) => entityExportUrl(entityType, entityId, t)" />
          <!--
            Duplicate. Gated on `inline_create` rather than `_actions` (there is
            no `create` key on an entity response); the mobile block below gates
            on the SAME computed via hasOverflow.
          -->
          <button v-if="canDuplicate" class="btn btn-secondary" @click="showDuplicateModal = true">
            Duplicate
          </button>
          <button v-if="canDelete" class="btn btn-danger" @click="requestDelete">
            Delete <kbd>Del</kbd>
          </button>
        </div>

        <!-- Mobile actions: Edit primary, delete icon, overflow menu for commands -->
        <div v-if="!props.hideActions" class="header-actions mobile-actions">
          <RouterLink v-if="editTarget" class="btn btn-secondary" :to="editTarget">
            Edit
          </RouterLink>
          <button
            v-if="canDelete"
            class="btn btn-danger mobile-delete-btn"
            aria-label="Delete"
            @click="requestDelete"
          >
            <svg
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            >
              <polyline points="3 6 5 6 21 6" />
              <path d="M19 6v14a2 2 0 01-2 2H7a2 2 0 01-2-2V6m3 0V4a2 2 0 012-2h4a2 2 0 012 2v2" />
            </svg>
          </button>
          <!--
            The overflow menu is the mobile home for EVERY header affordance
            that is not Edit or Delete. It used to hold commands only, so the
            world-era additions — FaceMenu, CopyMenu, History — were reachable
            on desktop and simply GONE on a phone: you could not publish a
            policy or switch language at all, with nothing on screen saying
            those actions exist.

            `hasOverflow` is one computed rather than a chain of `||` inline,
            so adding a fourth affordance means extending one expression that
            both the button and its contents read. The three-times-repeated
            omission was each author adding a control where they were already
            working; a single list is what makes "does this have a mobile
            home?" answerable in one place.
          -->
          <div v-if="hasOverflow" class="overflow-menu-wrapper">
            <button
              class="btn btn-secondary mobile-overflow-btn"
              aria-label="More actions"
              @click.stop="showOverflowMenu = !showOverflowMenu"
            >
              ⋯
            </button>
            <div v-if="showOverflowMenu" class="overflow-menu" @click="showOverflowMenu = false">
              <button
                v-for="cmd in commands"
                :key="cmd.id"
                class="overflow-menu-item"
                @click="runCommand(cmd)"
              >
                {{ cmd.label }}
              </button>
              <button
                v-for="a in detailActions"
                :key="`action-${a.id}`"
                class="overflow-menu-item"
                :disabled="detailActionBusy"
                @click="runDetailAction(a, $event)"
              >
                {{ a.label }}
              </button>
              <!--
                Faces and copies are rendered as flat rows here rather than as
                the FaceMenu/CopyMenu components: those open a nested dropdown,
                and a dropdown inside a dropdown on a phone is unusable. The
                affordance is the same and the handlers are shared, so a copy
                invoked from here goes through the identical guard.
              -->
              <button
                v-for="f in overflowFaces"
                :key="`face-${f.face}`"
                class="overflow-menu-item"
                @click="goToFace(f)"
              >
                View {{ f.label || f.face || 'default' }}
              </button>
              <button
                v-for="o in overflowCopies"
                :key="`copy-${o.name}`"
                class="overflow-menu-item"
                :disabled="copyBusy"
                @click="runCopy(o)"
              >
                {{ o.label || o.name }}
              </button>
              <button
                v-if="canDuplicate"
                class="overflow-menu-item"
                @click="showDuplicateModal = true"
              >
                Duplicate
              </button>
              <RouterLink v-if="showHistory" class="overflow-menu-item" :to="historyTarget">
                History
              </RouterLink>
            </div>
          </div>
        </div>
      </header>

      <!--
        Duplicate dialog. Mounted under v-if so the embedded create form inside
        it unmounts on close, aborting any in-flight dry-run rather than leaving
        it POSTing behind a closed dialog.

        `world` is threaded explicitly: it decides which face the copy lands in,
        and an embedded form reads props rather than the route.
      -->
      <DuplicateModal
        v-if="showDuplicateModal && entry && duplicateFormId"
        :source="entry"
        :form-id="duplicateFormId"
        :world="worldParam || undefined"
        @close="showDuplicateModal = false"
        @created="handleDuplicated"
      />

      <!-- Inaccessible (git-crypt encrypted) banner. Sits above the
           sections so it dominates the visual hierarchy when the entity
           cannot be read with the current key. -->
      <aside v-if="isInaccessible" class="inaccessible-banner">
        <span class="inaccessible-banner-icon" aria-hidden="true">🔒</span>
        <div>
          <strong>This entity is git-crypt encrypted.</strong>
          <p>
            The file is stored as ciphertext and cannot be read with the current configuration. Run
            <code>git-crypt unlock</code>
            in the project root to decrypt it, then reload this page.
            <a
              href="https://github.com/AGWA/git-crypt#readme"
              target="_blank"
              rel="noopener noreferrer"
              >Learn more about git-crypt.</a
            >
          </p>
        </div>
      </aside>

      <!-- Jump bar (only when there's enough to jump between) -->
      <nav v-if="viewData && viewData.sections.length > 1" class="jump-bar">
        <button
          v-for="section in viewData.sections.filter((s) => s.heading)"
          :key="section.sectionId"
          class="jump-link"
          @click="scrollToSection(section.sectionId)"
        >
          {{ section.heading }}
        </button>
      </nav>

      <!-- Sections -->
      <div v-if="viewData" class="sections">
        <section
          v-for="section in viewData.sections"
          :id="section.sectionId"
          :key="section.sectionId"
          class="view-section"
          :aria-labelledby="
            sectionRendersGenericHeading(section) ? `${section.sectionId}-heading` : undefined
          "
          :aria-label="
            sectionRendersGenericHeading(section) ? undefined : section.heading || undefined
          "
        >
          <!--
            The heading row carries the section's opt-in create affordance
            (TKT-R4BMJM). It sits HERE, above the six display branches, rather
            than inside each of them: the button is one affordance, and copying
            it into cards/list/table/nested/content/properties would be six
            places to keep in step — and six to unpick when FEAT-KQ45P unifies
            the branch structure.

            The row renders when there is a heading OR an affordance, so a
            section that opted in still gets its button when it has no heading.
          -->
          <div
            v-if="sectionRendersGenericHeading(section) || section.create"
            class="section-heading-row"
          >
            <h2
              v-if="sectionRendersGenericHeading(section)"
              :id="`${section.sectionId}-heading`"
              class="section-heading"
            >
              {{ section.heading }}
              <span v-if="section === entryContentSection && checkboxStats" class="cb-stats"
                >({{ checkboxStats.checked }}/{{ checkboxStats.total }})</span
              >
            </h2>
            <SectionCreateButton
              :create="section.create"
              :menu-label="'New'"
              @select="startCreate"
            />
          </div>

          <div v-if="section.isEmpty" class="section-empty">
            {{ section.emptyMessage || 'No items' }}
          </div>

          <!--
            Properties section routing (TKT-IHC7B):
            - At least one writable field → SectionEditForm (inline edit).
            - All non-writable                → PropertyDisplay (read-only).
            The `:key` on `${entry.type}/${entry.id}` forces SectionEditForm
            remount on entity-id change so its lifecycle handles route
            navigation cleanly (RR-FB1D + RR-FB2A).
          -->
          <SectionEditForm
            v-else-if="
              section.display === 'properties' &&
              entry &&
              sectionShouldRouteToInlineEdit(section, entry)
            "
            :key="`${entry.type}/${servedRef}`"
            :heading="section.heading"
            :entity-type="entry.type"
            :entity-id="servedRef"
            :initial-values="entry.properties"
            :attachments="entry._attachments"
            :fields="memoBuildSectionEditFields(section, entry)"
            :on-property-applied="handlePropertyApplied"
            :on-error="handleSectionEditError"
            :on-verdict-flip="handleVerdictFlip"
            :on-attachment-changed="loadView"
          />
          <PropertyDisplay
            v-else-if="section.display === 'properties'"
            :properties="mapFieldsToProperties(section.fields)"
          >
            <!-- Comment affordance per field (TKT-FIO205). Filled only here:
                 the same component renders list cells and kanban cards, where
                 a comment control would be noise. -->
            <template v-if="commentsEnabled" #label-affordance="{ property, index }">
              <CommentIndicator
                :entity-type="entityType"
                :entity-id="commentEntityId"
                :anchor="{ kind: 'property', ref: property.name }"
                :comments="commentsForProperty(property.name)"
                :flip="shouldFlipPopover(section.fields, index)"
                @changed="loadComments"
              />
            </template>
          </PropertyDisplay>

          <!-- Entry content with mermaid + interactive checkboxes.
               Function ref instead of string ref because this template lives
               inside a v-for: Vue would otherwise collect template-refs of
               the same name into an array per iteration. -->
          <div v-else-if="section === entryContentSection" class="entry-content-host">
            <div
              :ref="
                (el) => {
                  contentRef = el as HTMLElement | null
                }
              "
              class="content-body md-body"
              :data-comment-source="entryContentSection.content || ''"
              @click="contentClick"
              v-html="renderedEntryContent"
            />
            <!-- Select-to-comment over the body (TKT-FIO205 stage 2). Absolute
                 within .entry-content-host, so its offsets are relative to the
                 body rather than the viewport. -->
            <TextSelectionComment
              v-if="commentsEnabled"
              :entity-type="entityType"
              :entity-id="commentEntityId"
              :container="contentRef"
              @added="loadComments"
            />

            <!-- Comment affordances for blocks that cannot be text-selected:
                 images and mermaid/PlantUML diagrams. They anchor to the
                 block's SOURCE markdown, so they ride the same `text` kind. -->
            <BlockCommentOverlay
              v-if="commentsEnabled"
              :entity-type="entityType"
              :entity-id="commentEntityId"
              :container="contentRef"
              :render-key="renderedEntryContent"
              :comments="comments"
              @added="loadComments"
            />

            <!-- The thread for a clicked highlight. Anchored to the mark, which
                 lives in v-html output and so has no component of its own. -->
            <TextCommentPopover
              v-if="commentsEnabled && textCommentPos && openTextComments.length > 0"
              :entity-type="entityType"
              :entity-id="commentEntityId"
              :comments="openTextComments"
              :position="textCommentPos"
              :can-accept="canAccept"
              @changed="loadComments"
              @accept="acceptSuggestion"
              @close="closeTextComment"
            />
          </div>

          <!-- Other content sections (e.g. content cards from a configured view). -->
          <div
            v-else-if="section.display === 'content' && section.hasContent"
            class="content-block"
          >
            <div
              class="markdown-content md-body"
              v-html="renderMarkdown(section.content || '', refResolver)"
            />
          </div>

          <div
            v-else-if="section.display === 'content' && section.entities?.length"
            class="content-cards"
          >
            <article
              v-for="ent in section.entities"
              :key="ent.id"
              :data-entity-id="ent.id"
              class="content-card"
            >
              <header class="card-header">
                <component
                  :is="entityTarget(ent) ? RouterLink : 'span'"
                  class="card-header-link"
                  v-bind="entityTarget(ent) ? { to: entityTarget(ent) } : {}"
                >
                  <span class="entity-type">{{ ent.type }}</span>
                  <span class="entity-title">{{ ent.title }}</span>
                  <span class="entity-id">{{ ent.id }}</span>
                </component>
                <!--
                  Per-neighbour provenance (RULING 12/14). Each collection
                  entity resolved through the world INDEPENDENTLY, so one
                  section can mix `chain` and `fallback-default` — the badge is
                  per row, not per section. Renders nothing under the default
                  world.

                  Outside the link wrapper: the badge is provenance ABOUT the
                  row, not part of the link's accessible name — folding it in
                  would make the link read "Guide GUIDE-1 nl".
                -->
                <WorldBadge :world="ent._world" :entity-type="ent.type" />
              </header>
              <div
                v-if="ent.hasContent"
                class="markdown-content md-body"
                v-html="renderMarkdown(ent.content || '', refResolver)"
              />
            </article>
          </div>

          <div v-else-if="section.display === 'cards'" class="cards-grid">
            <article
              v-for="ent in section.entities"
              :key="ent.id"
              :data-entity-id="ent.id"
              class="entity-card"
            >
              <!--
                Navigation handler moved from <article> to .card-header
                (TKT-IHC7C / RR-FC1B): clicks on inline-edit widgets
                inside the card must not bubble to the row-level click
                because that would navigate away mid-edit. The header
                still navigates; cells stay editable.
              -->
              <header class="card-header">
                <component
                  :is="entityTarget(ent) ? RouterLink : 'span'"
                  class="card-header-link"
                  v-bind="entityTarget(ent) ? { to: entityTarget(ent) } : {}"
                >
                  <span class="entity-type">{{ ent.type }}</span>
                  <span class="entity-title">{{ ent.title }}</span>
                  <span class="entity-id">{{ ent.id }}</span>
                </component>
                <!-- Per-neighbour provenance; see the first WorldBadge above. -->
                <WorldBadge :world="ent._world" :entity-type="ent.type" />
                <button
                  v-if="ent.editFormId"
                  class="edit-btn"
                  title="Edit"
                  @click.stop="navigateToEdit(ent.editFormId, ent)"
                >
                  &times;
                </button>
              </header>
              <SectionEditForm
                v-if="rowShouldRouteToInlineEdit(ent, section.entities?.length ?? 0)"
                :key="`${ent.type}/${entityRef(ent)}`"
                :entity-type="ent.type"
                :entity-id="entityRef(ent)"
                :initial-values="ent._props ?? {}"
                :fields="memoBuildRowEditFields(ent)"
                :on-property-applied="handleRowPropertyApplied"
                :on-error="handleSectionEditError"
                :on-verdict-flip="handleVerdictFlip"
              >
                <!-- Indicator rendered inline in the card (TKT-IHC7C). NOT
                     teleported: the former <Teleport> target span lived in
                     the same render subtree as the Teleport itself, so Vue
                     could leave the teleported child orphaned with a null
                     instance that crashed on route-driven unmount (#997). -->
                <template #indicator="{ status, error }">
                  <span class="card-indicator-slot">
                    <AutoSaveIndicator :status="status" :error="error" />
                  </span>
                </template>
              </SectionEditForm>
              <div v-else-if="ent.fields?.length" class="card-fields">
                <div v-for="row in fieldRowsFor(ent)" :key="row.field.label" class="card-field">
                  <span class="field-label">{{ row.field.label }}:</span>
                  <!-- The wire-level inaccessibleReason map is keyed on
                       the entry's properties, not the per-entity card
                       row's. We don't have a per-card reason map today
                       (see RR-UD2E follow-up), so InaccessibleField
                       falls back to the generic tooltip. -->
                  <InaccessibleField v-if="row.field.inaccessible" />
                  <component
                    :is="row.widget"
                    v-else
                    :model-value="rowDisplayValue(ent, row.field)"
                    :mode="'display'"
                    :property-name="row.hint.propertyName"
                    class="field-value"
                  />
                </div>
              </div>
            </article>
          </div>

          <ul v-else-if="section.display === 'list'" class="entity-list">
            <li
              v-for="ent in section.entities"
              :key="ent.id"
              :data-entity-id="ent.id"
              class="list-item"
            >
              <component
                :is="entityTarget(ent) ? RouterLink : 'span'"
                class="list-link"
                v-bind="entityTarget(ent) ? { to: entityTarget(ent) } : {}"
              >
                <span class="entity-type">{{ ent.type }}</span>
                <span class="entity-title">{{ ent.title }}</span>
                <span class="entity-id">{{ ent.id }}</span>
              </component>
              <!-- Per-neighbour provenance; see the first WorldBadge above. -->
              <WorldBadge :world="ent._world" :entity-type="ent.type" />
              <SectionEditForm
                v-if="rowShouldRouteToInlineEdit(ent, section.entities?.length ?? 0)"
                :key="`${ent.type}/${entityRef(ent)}`"
                :entity-type="ent.type"
                :entity-id="entityRef(ent)"
                :initial-values="ent._props ?? {}"
                :fields="memoBuildRowEditFields(ent)"
                :on-property-applied="handleRowPropertyApplied"
                :on-error="handleSectionEditError"
                :on-verdict-flip="handleVerdictFlip"
              >
                <!-- Indicator rendered inline in the row (TKT-IHC7C). NOT
                     teleported: the former <Teleport> target span lived in
                     the same render subtree as the Teleport itself, so Vue
                     could leave the teleported child orphaned with a null
                     instance that crashed on route-driven unmount (#997). -->
                <template #indicator="{ status, error }">
                  <span class="list-indicator-slot">
                    <AutoSaveIndicator :status="status" :error="error" />
                  </span>
                </template>
              </SectionEditForm>
              <span v-else-if="ent.fields?.length" class="list-fields">
                <template v-for="row in fieldRowsFor(ent)" :key="row.field.label">
                  <!-- Per-card inaccessibility reason isn't on the wire
                       today (see RR-UD2E follow-up); InaccessibleField
                       falls back to the generic tooltip. -->
                  <InaccessibleField v-if="row.field.inaccessible" />
                  <component
                    :is="row.widget"
                    v-else
                    :model-value="rowDisplayValue(ent, row.field)"
                    :mode="'display'"
                    :property-name="row.hint.propertyName"
                  />
                </template>
              </span>
            </li>
          </ul>

          <div v-else-if="section.display === 'table'" class="table-wrapper">
            <template v-if="section.isGrouped && section.groups?.length">
              <div v-for="group in section.groups" :key="group.groupName" class="table-group">
                <h3 class="group-heading">{{ group.groupName }}</h3>
                <table class="data-table">
                  <thead>
                    <tr>
                      <th v-for="col in section.columns" :key="col.property || col.relation">
                        {{ col.label || col.property || col.relation }}
                      </th>
                      <th class="actions-col" />
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="row in group.rows" :key="row.entityId">
                      <td v-for="(cell, idx) in row.cells" :key="idx">
                        <a
                          v-if="cell.link"
                          :href="cell.link"
                          @click="
                            onCellLinkClick(
                              $event,
                              {
                                id: cell.entityId || row.entityId,
                                type: cell.entityType || row.entityType,
                              },
                              cell.link
                            )
                          "
                        >
                          <template v-for="(val, vidx) in cell.values" :key="vidx">
                            <Badge
                              v-if="shouldUseBadge(val, cell.propType)"
                              :value="val"
                              :property="cell.propType"
                            />
                            <span v-else>{{ val }}</span>
                            <span v-if="vidx < cell.values.length - 1">, </span>
                          </template>
                        </a>
                        <template v-else>
                          <template v-for="(val, vidx) in cell.values" :key="vidx">
                            <Badge
                              v-if="shouldUseBadge(val, cell.propType)"
                              :value="val"
                              :property="cell.propType"
                            />
                            <span v-else>{{ val }}</span>
                            <span v-if="vidx < cell.values.length - 1">, </span>
                          </template>
                        </template>
                      </td>
                      <td class="actions-cell">
                        <button
                          v-if="row.editFormId"
                          class="icon-btn"
                          title="Edit"
                          @click="
                            navigateToEdit(row.editFormId, { id: row.entityId, _self: row._self })
                          "
                        >
                          &#9998;
                        </button>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </template>

            <table v-else class="data-table">
              <thead>
                <tr>
                  <th v-for="col in section.columns" :key="col.property || col.relation">
                    {{ col.label || col.property || col.relation }}
                  </th>
                  <th class="actions-col" />
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in section.rows" :key="row.entityId">
                  <td v-for="(cell, idx) in row.cells" :key="idx">
                    <a
                      v-if="cell.link"
                      :href="cell.link"
                      @click="
                        onCellLinkClick(
                          $event,
                          {
                            id: cell.entityId || row.entityId,
                            type: cell.entityType || row.entityType,
                          },
                          cell.link
                        )
                      "
                    >
                      <template v-for="(val, vidx) in cell.values" :key="vidx">
                        <Badge
                          v-if="shouldUseBadge(val, cell.propType)"
                          :value="val"
                          :property="cell.propType"
                        />
                        <span v-else>{{ val }}</span>
                        <span v-if="vidx < cell.values.length - 1">, </span>
                      </template>
                    </a>
                    <template v-else>
                      <template v-for="(val, vidx) in cell.values" :key="vidx">
                        <Badge
                          v-if="shouldUseBadge(val, cell.propType)"
                          :value="val"
                          :property="cell.propType"
                        />
                        <span v-else>{{ val }}</span>
                        <span v-if="vidx < cell.values.length - 1">, </span>
                      </template>
                    </template>
                  </td>
                  <td class="actions-cell">
                    <button
                      v-if="row.editFormId"
                      class="icon-btn"
                      title="Edit"
                      @click="
                        navigateToEdit(row.editFormId, { id: row.entityId, _self: row._self })
                      "
                    >
                      &#9998;
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- display: nested — a two-level parent→child tree.
               Native <details> rather than a JS-managed expanded set: the
               browser gives keyboard support, find-in-page expansion and the
               correct ARIA for free, and the section has no cross-row state
               that would need lifting. -->
          <!-- The server cut rows at the node cap. Without saying so the list
               reads as complete, which is the whole reason the flag exists. -->
          <div v-else-if="section.display === 'nested'" class="nested-tree-wrap">
            <p v-if="section.truncated" class="nested-truncated">
              Some rows were not shown because this section hit its size limit.
            </p>
            <div class="nested-tree">
              <!-- Open by default: a collapsed tree hides the thing the section
                   exists to show, and the node budget already bounds how much
                   arrives. Native <details> keeps keyboard support,
                   find-in-page expansion and the right ARIA for free. -->
              <details v-for="node in section.tree" :key="node.entity.id" class="nested-node" open>
                <summary class="nested-row">
                  <ChevronRight class="nested-twisty" :size="18" aria-hidden="true" />
                  <!-- .stop so following the link does not ALSO toggle the
                       disclosure: Chrome routes <summary> activation through the
                       bubbling click, so stopping it there suppresses the toggle
                       while the twisty and the rest of the row still expand.
                       The child link below needs no .stop — it is not inside a
                       <summary>, so it has nothing to suppress. -->
                  <component
                    :is="entityTarget(node.entity) ? RouterLink : 'span'"
                    class="nested-link"
                    v-bind="entityTarget(node.entity) ? { to: entityTarget(node.entity) } : {}"
                    @click.stop
                  >
                    <span class="entity-title">{{ node.entity.title }}</span>
                    <span class="entity-id">{{ node.entity.id }}</span>
                  </component>
                  <WorldBadge :world="node.entity._world" :entity-type="node.entity.type" />
                  <span class="nested-values">
                    <component
                      :is="cell.widget"
                      v-for="cell in nestedCellsFor(node)"
                      :key="cell.property"
                      :model-value="cell.value"
                      :mode="'display'"
                      :property-name="cell.propertyName"
                    />
                  </span>
                </summary>

                <div v-if="node.children?.length" class="nested-children">
                  <div
                    v-for="child in node.children"
                    :key="child.entity.id"
                    :data-entity-id="child.entity.id"
                    class="nested-child"
                  >
                    <component
                      :is="entityTarget(child.entity) ? RouterLink : 'span'"
                      class="nested-link"
                      v-bind="entityTarget(child.entity) ? { to: entityTarget(child.entity) } : {}"
                    >
                      <span class="entity-title">{{ child.entity.title }}</span>
                      <span class="entity-id">{{ child.entity.id }}</span>
                    </component>
                    <WorldBadge :world="child.entity._world" :entity-type="child.entity.type" />
                    <span class="nested-values">
                      <component
                        :is="cell.widget"
                        v-for="cell in nestedCellsFor(child)"
                        :key="cell.property"
                        :model-value="cell.value"
                        :mode="'display'"
                        :property-name="cell.propertyName"
                      />
                    </span>
                  </div>
                </div>
                <div v-else class="nested-children nested-empty">No items</div>

                <!-- The server withheld children; say so rather than letting the
                     list read as complete. -->
                <div v-if="node.hasMoreChildren" class="nested-children nested-more">
                  Showing {{ node.children?.length ?? 0 }} of {{ node.childCount }}
                </div>
              </details>
            </div>
          </div>
        </section>

        <!-- External documents (renders only when configured for this type).
             Nested here (not a sibling of .sections) so it picks up the
             container's flex `gap` for free instead of duplicating that
             spacing via its own margin. -->
        <DocumentsPanel :entity-type="entityType" :entity-id="entityId" />

        <!-- Comment thread. Self-gating: renders nothing unless the schema
             marks this type commentable, so a project with no `comments:`
             block sees the page it always saw. -->
        <CommentsPanel
          :entity-type="entityType"
          :entity-id="commentEntityId"
          :comments="comments"
          :section-ids="commentSectionIds"
          :can-accept="canAccept"
          @changed="loadComments"
          @accept="acceptSuggestion"
        />
      </div>

      <!-- The ADDRESS, not the bare id: a command acts on the face on screen. -->
      <CommandModal ref="commandModalRef" :entity-id="servedRef" />

      <!--
        The modal create host. Reused verbatim from the inline-create flow
        (TKT-OMUD56) rather than reimplemented: it already owns the dirty-discard
        confirm, the focus restore, the modal stack and the nesting cap, and it
        depends on none of the form context it was first written inside.

        `v-if` rather than `v-show` — unmounting is what aborts the embedded
        form's in-flight dry run; a hidden-but-alive form keeps POSTing behind a
        closed dialog.
      -->
      <InlineCreateFormModal
        v-if="activeCreate"
        :show="true"
        :form-id="activeCreate.target.formId"
        :entity-type="activeCreate.target.entityType"
        :template="activeCreate.target.template"
        :link="{
          relation: activeCreate.create.relation,
          peer: activeCreate.create.peerId,
          linkAs: activeCreate.create.linkAs,
        }"
        :world="worldParam || undefined"
        @close="closeCreate"
        @created="onCreated"
      />
    </template>

    <div v-else class="error-state">
      <h2>Entity not found</h2>
      <p>{{ entityType }} "{{ entityId }}" could not be found.</p>
      <RouterLink :to="backTargetAfterDelete()" class="btn btn-secondary">
        Back to list
      </RouterLink>
    </div>
  </div>
</template>

<style scoped>
/* Positioning context for the select-to-comment popup, so its coordinates are
 * relative to the body rather than the viewport (which would drift on scroll). */
.entry-content-host {
  position: relative;
}

/* Text-anchored comment highlights (TKT-FIO205 stage 2).
 *
 * :deep() because the marks are inserted into v-html output, which scoped-style
 * hashing does not reach. */
.content-body :deep(mark[data-comment-id]) {
  /* Yellow, the annotation convention — and distinct from the accent blue that
   * already marks links and interactive chrome in the body. */
  background: color-mix(in srgb, var(--comment-highlight) 32%, transparent);
  border-bottom: 2px solid var(--comment-highlight);
  border-radius: 2px;
  padding: 0 1px;
  color: inherit;
  cursor: pointer;
}

/* A highlighted LINK keeps its own click: navigation is the primary action and
 * a mark must not swallow it. The chip beside it opens the thread instead. */
.content-body :deep(mark[data-comment-id] a) {
  cursor: pointer;
}

.content-body :deep(.comment-chip) {
  display: inline-flex;
  align-items: center;
  vertical-align: super;
  margin-left: 2px;
  padding: 0 4px;
  border: 0;
  border-radius: 8px;
  background: var(--comment-highlight);
  color: var(--text-color);
  font-size: 10px;
  line-height: 1.5;
  cursor: pointer;
}

/* An uncertain anchor resolved below the exact band: the text may have moved,
 * so it reads as provisional rather than as a confirmed location. */
.content-body :deep(mark[data-comment-uncertain]) {
  background: color-mix(in srgb, var(--warning-color) 34%, transparent);
  border-bottom-style: dashed;
  border-bottom-color: var(--warning-color);
}

.entity-detail {
  max-width: 1200px;
  padding: 0 0 24px;
}

.inaccessible-banner {
  display: flex;
  align-items: flex-start;
  gap: var(--space-md);
  margin-bottom: 24px;
  padding: 12px 16px;
  border: 1px solid var(--color-border, #ccc);
  border-left: 4px solid var(--color-warning, #d9970e);
  border-radius: var(--radius-sm);
  background: var(--color-surface, #fafafa);
}

.inaccessible-banner-icon {
  font-size: 20px;
  line-height: 1;
}

.inaccessible-banner p {
  margin: 4px 0 0;
  font-size: var(--font-size-base);
  line-height: 1.5;
}

.inaccessible-banner code {
  padding: 1px 4px;
  background: var(--color-code-bg, #eee);
  border-radius: 3px;
  font-family: var(--font-mono, monospace);
}

/* Uses global .loading-state, .error-state, .spinner from App.vue */

/* The world banner's notes. Two may render at once (`notice` about the
   document, `read_only` about the reader), and they are separate sentences
   from separate config keys — so they stack rather than reflowing into one
   line, whatever the operator's punctuation.

   jsdom does no layout, so no unit test sees any of this: deleting this block
   passes the whole suite. Verified by hand instead, measuring the rendered
   geometry with both keys declared long enough to wrap, beside a world that
   declares a `banner:` label. At full width the notes sit beside the label,
   two 18px lines with the 4px gap. Below WorldBanner's `24rem` flex basis the
   wrapper wraps under the label as that basis intends, each note takes two
   lines, and the banner grows to contain them — no overlap, no clipping. */
.banner-note {
  /* The parent `.world-banner__note` is a `<span>`, but it is a flex ITEM and
     therefore blockified, so these children lay out as blocks inside it. */
  display: block;
}

.banner-note + .banner-note {
  margin-top: var(--space-2xs);
}

/* Header */
.detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-lg);
  margin-bottom: 24px;
}

.header-info {
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
}

.entity-type-badge {
  display: inline-block;
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--muted-text);
}

.header-info h1 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
  color: var(--text-color);
}

.header-actions {
  display: flex;
  gap: var(--space-sm);
}

.header-actions kbd {
  padding: 2px 5px;
  font-size: 10px;
  background: var(--border-color);
  border-radius: 3px;
  font-family: monospace;
  margin-left: 4px;
}

.btn-command {
  background: var(--accent-color, #3b82f6);
  color: white;
}

.btn-command:hover:not(:disabled) {
  filter: brightness(1.1);
}

/* Mobile-responsive header. .desktop-actions and .mobile-actions are
 * toggled by media queries in App.vue's global styles. */
.mobile-actions {
  display: none;
}

@media (max-width: 720px) {
  .desktop-actions {
    display: none;
  }
  .mobile-actions {
    display: flex;
    align-items: center;
  }
}

.mobile-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 6px 10px;
}

.overflow-menu-wrapper {
  position: relative;
}

.mobile-overflow-btn {
  font-size: var(--font-size-lg);
  line-height: 1;
  padding: 6px 12px;
}

.overflow-menu {
  position: absolute;
  right: 0;
  top: calc(100% + 4px);
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  min-width: 160px;
  z-index: 50;
}

.overflow-menu-item {
  display: block;
  width: 100%;
  padding: 8px 12px;
  background: none;
  border: none;
  text-align: left;
  font-size: var(--font-size-base);
  color: var(--text-color);
  cursor: pointer;
}

.overflow-menu-item:hover {
  background: var(--hover-bg);
}

/* Scope Navigation Bar
 * .scope-nav-btn styles live in src/styles/back-button.css — see TKT-JIEKC. */
.scope-nav {
  display: flex;
  align-items: center;
  gap: var(--space-md);
  margin-bottom: 20px;
}

.scope-nav-progress {
  font-size: var(--font-size-dense);
  font-weight: 600;
  color: var(--text-color);
  font-family: monospace;
}

.scope-nav-label {
  flex: 1;
  font-size: var(--font-size-dense);
  color: var(--muted-text);
}

/* Jump bar */
.jump-bar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-sm);
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color);
  margin-bottom: 24px;
}

.jump-link {
  padding: 6px 12px;
  background: var(--hover-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-dense);
  color: var(--text-color);
  cursor: pointer;
  transition: all 0.15s;
}

.jump-link:hover {
  background: var(--accent-color);
  border-color: var(--accent-color);
  color: white;
}

/* Sections */
.sections {
  display: flex;
  flex-direction: column;
  gap: var(--space-2xl);
}

.view-section {
  scroll-margin-top: 20px;
}

/* KEEP IN SYNC with SectionEditForm.vue `.section-edit-form-header
   .section-heading` (RR-ZE29PY): the properties inline-edit section renders
   its heading inside that component, which can't reach these scoped styles. */
.section-heading-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--space-md);
}

/* The row owns the heading's spacing when both are present; the h2 keeps its
   own margins so a section with no create affordance is unchanged. */
.section-heading-row > .section-heading {
  flex: 1 1 auto;
}

.section-heading {
  font-size: var(--font-size-lg);
  font-weight: 600;
  margin: 0 0 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-color);
  color: var(--text-color);
}

.cb-stats {
  font-size: var(--font-size-base);
  font-weight: 500;
  color: var(--muted-text);
  margin-left: 8px;
}

.section-empty {
  padding: 24px;
  text-align: center;
  color: var(--muted-text);
  background: var(--hover-bg);
  border-radius: var(--radius-md);
  font-style: italic;
}

/* Rendered-markdown element styling (headings, lists, code, links, tables,
   blockquotes, line-height, …) is shared across all markdown surfaces via the
   `.md-body` class on the container — see styles/markdown-content.css. Both
   `.content-body` and `.markdown-content` carry that class, so neither
   redefines those properties here (an earlier `.markdown-content` line-height
   override silently beat the shared value and reintroduced drift). */

/* Generic content (collected entities, configured-view content sections). */
.content-block {
  padding: 16px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
}

.content-cards {
  display: flex;
  flex-direction: column;
  gap: var(--space-lg);
}

.content-card {
  padding: 16px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
}

.content-card .card-header {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  margin-bottom: 12px;
  /* No cursor: pointer — the header is no longer clickable as a whole; the
     pointer comes from .card-header-link, so the padding beside it must not
     advertise a click that does nothing. */
}

.content-card .card-header:hover .entity-title {
  color: var(--accent-color);
}

/* Cards grid (relation sections etc.) */
.cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: var(--space-lg);
}

/* Mirrors EntityList's world banner so the two surfaces read as one feature.
   Horizontal here (the detail page has the width and the note is shorter),
   with the leave-world button on the trailing edge. */

.entity-card {
  padding: 16px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: border-color 0.15s;
}

.entity-card:hover {
  border-color: var(--accent-color);
}

.card-header {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  margin-bottom: 12px;
}

.entity-type {
  font-size: 10px;
  text-transform: uppercase;
  color: var(--muted-text);
  background: var(--border-color);
  padding: 2px 4px;
  border-radius: 2px;
}

.entity-title {
  font-weight: 500;
  color: var(--text-color);
  flex: 1;
}

.entity-id {
  font-size: var(--font-size-xs);
  font-family: monospace;
  color: var(--muted-text);
}

.edit-btn {
  background: none;
  border: none;
  color: var(--muted-text);
  font-size: 16px;
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
}

.edit-btn:hover {
  background: var(--hover-bg);
  color: var(--text-color);
}

.card-fields {
  display: flex;
  flex-direction: column;
  gap: var(--space-2xs);
}

.card-field {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  font-size: var(--font-size-dense);
}

.field-label {
  color: var(--muted-text);
}

.field-value {
  color: var(--text-color);
}

/* Entity list */
.entity-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
}

.list-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-md);
  padding: 8px 12px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
}

.list-link {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  cursor: pointer;
  flex: 1;
  /* Was an <a> with no href (so cmd-click did nothing); now a real RouterLink.
     Keep the inherited text colour and no underline. */
  color: inherit;
  text-decoration: none;
}

/* The link inside a card header carries the header's own flex layout, so the
   header still reads as one row. It takes the free space, leaving the
   edit-button (which may NOT live inside an <a>) a sibling beside it. */
.card-header-link {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  flex: 1;
  min-width: 0;
  cursor: pointer;
  color: inherit;
  text-decoration: none;
}

.list-link:hover .entity-title {
  color: var(--accent-color);
}

.list-fields {
  display: flex;
  gap: var(--space-xs);
}

/* AutoSaveIndicator slot for inline-edit rows (TKT-IHC7C). Rendered inline
   in the row's SectionEditForm rather than teleported (#997). Pinned to the
   top-right of the form so it reads as inline-right of the row content,
   matching the pre-teleport placement. */
.list-indicator-slot,
.card-indicator-slot {
  position: absolute;
  top: -28px;
  right: 0;
  display: inline-flex;
  align-items: center;
}

/* Table */
.table-wrapper {
  overflow-x: auto;
}

/* display: nested — parent rows that expand to their children. */
.nested-truncated {
  margin: 0 0 8px;
  font-size: var(--font-size-sm);
  color: var(--text-color);
  background: var(--hover-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm);
  padding: 6px 10px;
}

.nested-tree {
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  overflow: hidden;
}

.nested-node:not(:last-child) {
  border-bottom: 1px solid var(--border-color);
}

.nested-row {
  display: flex;
  align-items: center;
  gap: var(--space-md);
  padding: 8px 12px;
  background: var(--card-bg);
  cursor: pointer;
  /* The default triangle would sit beside our own twisty. */
  list-style: none;
}

.nested-row::-webkit-details-marker {
  display: none;
}

.nested-row:hover {
  background: var(--hover-bg);
}

.nested-twisty {
  flex: none;
  color: var(--muted-text);
  transition: transform 0.15s ease;
}

.nested-node[open] > .nested-row .nested-twisty {
  transform: rotate(90deg);
}

.nested-link {
  display: flex;
  align-items: baseline;
  gap: var(--space-sm);
  min-width: 0;
  text-decoration: none;
  color: inherit;
}

.nested-link:hover .entity-title {
  text-decoration: underline;
}

/* Column values sit right-aligned at the end of the row. Each is rendered by
   its property's widget, so an enum arrives as a Badge and a date as a
   formatted string — the same treatment every other surface gives it. */
.nested-values {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: var(--space-md);
  flex: none;
  font-size: var(--font-size-sm);
  color: var(--muted-text);
  white-space: nowrap;
}

.nested-children {
  border-top: 1px solid var(--border-color);
  background: var(--bg-color);
}

.nested-child {
  display: flex;
  align-items: center;
  gap: var(--space-md);
  padding: 6px 12px 6px 32px;
  font-size: var(--font-size-dense);
}

.nested-child:not(:last-child) {
  border-bottom: 1px solid var(--border-color);
}

.nested-child:hover {
  background: var(--hover-bg);
}

.nested-empty,
.nested-more {
  padding: 6px 12px 6px 32px;
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}

.nested-more {
  border-top: 1px solid var(--border-color);
}

.table-group {
  margin-bottom: 24px;
}

.group-heading {
  font-size: var(--font-size-base);
  font-weight: 600;
  color: var(--muted-text);
  margin: 0 0 8px;
  padding: 4px 0;
  border-bottom: 1px solid var(--border-color);
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-base);
}

.data-table th,
.data-table td {
  padding: 10px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border-color);
}

.data-table th {
  font-weight: 500;
  color: var(--muted-text);
  background: var(--hover-bg);
}

.data-table td {
  color: var(--text-color);
}

.data-table tbody tr:hover {
  background: var(--hover-bg);
}

.data-table a {
  color: var(--accent-color);
  text-decoration: none;
}

.data-table a:hover {
  text-decoration: underline;
}

.actions-col {
  width: 60px;
}

.actions-cell {
  text-align: center;
}

.icon-btn {
  background: none;
  border: none;
  color: var(--muted-text);
  cursor: pointer;
  padding: 4px 8px;
  font-size: var(--font-size-base);
  border-radius: var(--radius-sm);
}

.icon-btn:hover {
  background: var(--hover-bg);
  color: var(--text-color);
}

@media (max-width: 768px) {
  /* .scope-nav uses .mobile-topbar from mobile-bars.css for the sticky
     chrome + safe-area math. Override only the layout: dense (no gap,
     no wrap) so prev/progress/next sit flush across the bar. */
  .scope-nav {
    gap: 0;
    flex-wrap: nowrap;
    justify-content: space-between;
    /* The bar uses 12px horizontal padding here instead of the default
       16 — the prev/next buttons hug the edges. */
    padding-left: 12px;
  }

  .desktop-actions {
    display: none;
  }

  /* Inline mobile actions (Edit / delete / overflow) — comfortable touch
     targets and spacing. Toggled visible via the .mobile-actions rules
     above. */
  .mobile-actions {
    display: flex;
    gap: var(--space-sm);
    align-items: center;
  }

  .mobile-actions .btn {
    min-height: 44px;
  }

  .mobile-actions .btn-secondary {
    flex: 1;
  }

  .header-info h1 {
    font-size: var(--font-size-xl);
  }

  .detail-section {
    background: none;
    border: none;
    box-shadow: none;
    border-radius: 0;
    padding: 0;
    margin-bottom: 20px;
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 16px;
  }
}
</style>
