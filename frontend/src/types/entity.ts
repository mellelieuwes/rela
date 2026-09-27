// Minimal shape a single-value entity-target selector needs: an id and an
// optional backend-supplied display title. Entity satisfies it structurally,
// so list rows can be passed straight through. Lives here so both the
// component and its callers/tests import it from one place.
export interface TargetCandidate {
  id: string
  _title?: string
}

export interface Entity {
  id: string
  type: string
  _title?: string
  properties: Record<string, unknown>
  content?: string
  relations?: Record<string, string[]>
  included?: Record<string, Entity>
  _self?: string
  // Per-resource verb-verdict map driven by the backend ACL. Keys are
  // verbs (phase 1: `update`, `delete`, `rename` per-item; `create`
  // on collection responses); values are booleans. Always present
  // on responses from the data-entry server. An empty map means the
  // principal has every verb denied — UI should hide all affordances.
  // See .ignored/action-affordances-design.md for the full contract.
  _actions?: Record<string, boolean>
  // Per-field write affordances on per-entity GET responses.
  // Sparse: only fields whose verdict deviates from default appear.
  // Hidden fields are omitted from `properties` AND from `_fields` —
  // to learn WHICH fields are hidden, read `_redacted`, never absence.
  // Absent on list / mutation responses; present (possibly empty) on
  // per-entity GET (closed-world signal — empty means "evaluated, no
  // deviations"). See docs/data-entry/api-reference.md.
  _fields?: Record<string, FieldAffordance>
  // Property names withheld from `properties` by field-level ACL
  // (`visible:`) on this response — the field-level sibling of
  // `inaccessible` (DEC-T0XIWQ).
  //
  // This exists because absence from `properties` is ambiguous: a key can
  // be missing because it was redacted OR because it was never set. Read
  // surfaces may conflate those; a write form must not, or it either hides
  // a field the user can legitimately fill (BUG-MLT9DE) or offers a
  // redacted one as an apparently-empty input and clobbers the stored
  // value on save. NEVER infer redaction from absence — consult this list.
  //
  // Names only; values stay withheld. Present (possibly empty) on
  // per-entity responses, absent on list rows.
  _redacted?: string[]
  // Whether a write may change `content` (the markdown body). `false` when an
  // external system owns the body through a twin; the server then refuses a
  // changed body with 422 and the body editor renders disabled. Present on
  // per-entity responses, absent on list rows — only `false` disables.
  content_writable?: boolean
  // Per-relation-type affordances on per-entity GET responses. Same
  // sparse / closed-world semantics as _fields. Per-relation-type
  // uniform — per-link verdicts are predicate territory (deferred).
  _relations?: Record<string, RelationAffordance>
  // Per-`file`-property attachment metadata on per-entity responses.
  // Keyed by property name; the value is always a LIST (a property may
  // hold several files when its metamodel `max` > 1, and a single file is
  // a 1-element list). Only properties that carry a file appear. Same
  // closed-world semantics as _fields/_relations: present (possibly empty)
  // on every per-entity response (GET, PATCH, POST, clone), absent on list
  // rows. The file widget reads this to render download links / previews.
  _attachments?: Record<string, AttachmentInfo[]>
  // Per-state-machine-field transition verdicts on per-entity GET
  // responses (TKT-3G93B8). Keyed by property name; the value lists the
  // outgoing transitions resolved for the requesting principal on this
  // entity. Only state-machine-typed fields appear — a plain enum field
  // has no key, and the SPA falls back to the ordinary enum control.
  // Absent entirely when the server wires no state machines (older server
  // or no policy). A UI hint, never authorization — the write path
  // re-enforces every transition (attempt-and-recover). See
  // docs/data-entry/api-reference.md.
  _transitions?: Record<string, TransitionOption[]>
  // Provenance of the face this response served (TKT-WRLDAPI). Present on
  // per-entity GETs under any world INCLUDING the default one, where it reads
  // `{name:'default', via:'unscoped'}`. Absent on list rows and on the
  // `_views` ENTRY — under `_views` only COLLECTION entities carry it
  // (internal/dataentry/sections.go is the single call site), so a detail page
  // wanting entry provenance reads it from the entity GET.
  _world?: EntityWorld
  // Declared copies offered for this face (TKT-WRLDAPI item 5). Rides the
  // entity response alongside `_actions` rather than a separate endpoint.
  // `[]` is a real answer ("this face offers no copies"); absent means the
  // server computed none (no copy service wired). Both render nothing, but
  // only `[]` means the question was asked.
  _copies?: CopyOffer[]
  // Other content states this entity has. See Face.
  _faces?: Face[]
  inaccessible?: InaccessibleField[]
  // Soft-validation findings on mutation responses (DEC-HWZHA).
  // Present on PATCH/POST results; absent on GETs.
  warnings?: Warning[]
  // Entity-ID code spans in `content`, resolved to their titles so an EDIT
  // surface can render them as links the way the read surface does. Same map
  // and same semantics as `ViewResponse.mentions` — the server produces both
  // from one call, so the two surfaces cannot disagree about a title.
  //
  // Present on the single-entity GET only; a list row has no content to scan.
  // Per-principal: an entity the caller may not read has NO entry (the code
  // span stays plain), and one whose title is redacted arrives with
  // `inaccessible` set and its ID as the title. Never derive a title another
  // way — that is what this map exists to prevent.
  mentions?: Record<string, Mention>
}

// Mention resolves one entity-ID code span to its display title. Mirrors the
// server-side `v1.Mention` (TKT-747O). `inaccessible` flags a target whose
// display title is unreadable (e.g. git-crypt encrypted) so a renderer can
// show a lock affordance instead of a name it does not have.
//
// `inaccessible_reason` carries the matching `entity.InaccessibleReason` value
// as a bare string. Today only `"git-crypt"` is produced; clients treat an
// unknown reason as opaque and fall back to a generic tooltip, so new reasons
// server-side never break the client.
export interface Mention {
  type: string
  title: string
  inaccessible?: boolean
  inaccessible_reason?: string
}

// FieldAffordance carries per-field write / option affordances on
// the wire. Sparse: `writable` undefined means default (writable);
// `options` lists only the false entries (allowed options are
// implicit via the metamodel).
//
// `reason` explains a `writable: false` verdict when its cause is more
// specific than policy read-only — "owned by basecamp" for a field an
// external system owns through a twin.
export interface FieldAffordance {
  writable?: boolean
  options?: Record<string, boolean>
  reason?: string
}

// RelationAffordance carries per-relation-type affordances on the
// wire. Same sparse semantics as FieldAffordance: `creatable` /
// `removable` undefined means default (true). `fields` is the
// per-meta-field writability map, also sparse.
export interface RelationAffordance {
  creatable?: boolean
  removable?: boolean
  fields?: Record<string, FieldAffordance>
}

// TransitionOption is one resolved outgoing move of a state-machine field
// (TKT-3G93B8), the wire projection of the backend's TransitionVerdict.
// `to` is the target value; `label` is optional display text for the MOVE
// (the action, e.g. "Start progress") — when absent the UI falls back to the
// target value's display label. `guard` names the permission the move
// requires (empty when unguarded). `allowed` reports whether the requesting
// principal may perform it now. `reason` names the blocking gate when not
// allowed (`guard` | `precondition`); empty when allowed. The status control
// shows only allowed moves, so `reason` is advisory (tooltip), not a gate.
export interface TransitionOption {
  to: string
  label?: string
  guard?: string
  allowed: boolean
  reason?: string
}

// EntityWorld is the provenance of a resolved face: which world was asked
// for, which coordinate was served, and WHICH RULE chose it. Mirrors
// v1.EntityWorld (internal/apiwire/v1/responses.go).
//
// `via` is the load-bearing field, and the reason this type exists rather
// than a bare world name. Under a world with `otherwise: default`, "the
// Dutch page" and "the English page, because no Dutch page exists" arrive
// BYTE-IDENTICALLY — same id, same body, same everything. Only `via`
// separates them:
//
//   - 'unscoped'         — no resolution was applied: the type declares no
//                          faces, the default world is in force, or the
//                          request ADDRESSED the face (`POL-1@published`),
//                          in which case the world made no choice and the
//                          address did. Never a stand-in.
//   - 'chain'            — SOME coordinate the world selects exists. Note
//                          this does NOT say which: see `chain_position`.
//   - 'fallback-default' — no selected coordinate exists and the world's
//                          `otherwise: default` stood the default state in.
//                          The reader is seeing a SUBSTITUTE.
//
// There is deliberately no 'excluded': a world that excludes an entity
// produces no response to carry provenance on. It is a 404, indistinguishable
// from a genuine miss, because existence in a world IS the publication bit.
export interface EntityWorld {
  name: string
  face: string
  via: 'unscoped' | 'chain' | 'fallback-default'

  /**
   * 0-based index, within the world's candidate chain for this entity type,
   * of the coordinate actually served. Present only when `via` is 'chain'.
   *
   * `via` alone cannot answer "did I get the world's first choice". Under
   * `select: [published, draft]` a real published face and a draft standing
   * in for a missing one BOTH report 'chain', so a reader shown draft bytes
   * under a `published` world had no way to tell — observed live, with the
   * page framing the draft as read-only published content.
   *
   * 0 is the world's first choice. Anything greater is a WITHIN-CHAIN
   * FALLBACK and must be labelled as a substitute, exactly as
   * 'fallback-default' is.
   *
   * Optional so a response from an older server (which omits it) is not
   * mistaken for position 0 — the strongest claim the field makes.
   */
  chain_position?: number
}

// Face is one content state this entity HAS, other than the one being served.
// Mirrors v1.Face.
//
// Existence only — no readability flag, deliberately. World-read is a GLOBAL,
// role-level grant already held from `/_schema`.worlds, so a per-face flag
// would re-answer a question asked once for the whole session. A face the
// principal cannot read still appears; clicking it lands on the ordinary row
// gate, the same answer a typed URL gives.
export interface Face {
  // The STORED coordinate ("" is the default face) — what a client sends back
  // when addressing the face.
  face: string
  // The declared face name, for display.
  label?: string
  // The face's ADDRESS: the path segment that reads this row literally under
  // any world (`POL-1@published`; `POL-1@draft` for the bare face when the
  // type names one). A client offers "view the published face" as a plain
  // link to it, without working out which world leads with that face. A bare
  // face with no declared name has no explicit spelling and falls back to the
  // bare id, which is literal only in the default world. Absent on an older
  // server.
  ref?: string
}

// CopyOffer is one declared `copies:` definition offered for the face being
// viewed. Mirrors v1.CopyOffer.
//
// `allowed` is a HINT, never a boundary — the same contract as `_actions` and
// `TransitionOption`. The invoke endpoint re-authorizes through the kernel, so
// a client that ignores this and POSTs anyway gets the same 403 it would have
// received regardless. It is computed by running the kernel's own
// authorization path, so it cannot drift from what the write does.
//
// The UI renders only allowed offers, which makes `reason` advisory (a
// tooltip for a debugging operator), not a gate.
export interface CopyOffer {
  // The `copies:` key, and the ONLY thing a client sends back to invoke it.
  // A request names a definition; it never supplies one.
  name: string
  // Operator-configured display text; falls back to `name` when unset.
  label: string
  // The declared target (`policy@published`) — for a UI that wants to say
  // what the action will produce. Always another face of the SAME entity:
  // the server offers no cross-entity definitions, so an invoke needs no
  // target id.
  targetFace: string
  allowed: boolean
  reason?: string
  // The operator's follow-through (TKT-5SZG2L): the toast (absent means the
  // copy's label) and where to land. Absent means land on the face written.
  onSuccess?: CopyOnSuccess
}

export interface CopyOnSuccess {
  message?: string
  landing: { mode: 'written' | 'stay' | 'world' | 'face'; world?: string; face?: string }
}

// AttachmentInfo describes one file attached to a `file`-type property,
// as surfaced in Entity._attachments. `id` identifies the file within the
// property (its normalized name) and is used to build the per-file
// download/delete URL. `href` is the ACL-gated download URL (inherits the
// entity's read permission). `contentType` is inferred from the filename
// by the backend.
export interface AttachmentInfo {
  id: string
  filename: string
  size: number
  contentType: string
  href: string
}

// Warning is a soft validation finding returned alongside a successful
// mutation. Code matches the analyze_* finding code so UIs can
// de-duplicate. See docs/data-entry/api-reference.md for stable codes.
export interface Warning {
  code: string
  path: string
  detail: string
}

// JSON:API §9 resource identifier — the per-edge shape inside the
// unified PATCH's modern relations field. Used by the patch builder
// to emit edges with explicit type, meta, and (future) content.
export interface ResourceIdentifier {
  type: string
  id: string
  meta?: Record<string, unknown>
  meta_unset?: string[]
  content?: string
}

// Modern relations field shape for the unified PATCH body. Keys are
// relation names; each value's `data` is the desired set of edges.
// Sending `data: []` clears all edges of that type — see the
// data-loss footgun docs in docs/data-entry/api-reference.md.
export interface ModernRelationsField {
  [relationName: string]: { data: ResourceIdentifier[] }
}

// InaccessibleField marks a property whose value is known to exist but is
// unreadable by the holder of the entity (e.g. the file is git-crypt
// encrypted and the key is not present locally). The SPA renders such
// fields with a lock indicator instead of an editable widget.
export interface InaccessibleField {
  name: string
  reason: string
}

export interface CreateEntity {
  id?: string
  prefix?: string
  // The world the create was issued from. The server resolves it to the face
  // `worlds.<name>.create` names; a faced type has no default row, so without
  // it the create is refused. Rides the body because `?world=` is refused on
  // every write (a read chain can answer with a fallback).
  world?: string
  properties: Record<string, unknown>
  content?: string
  // Modern JSON:API §9 wrapper shape only. The legacy IDs-only form
  // (`Record<string, string[]>`) is no longer accepted on the wire.
  relations?: ModernRelationsField
}

export interface RelationEntry {
  id: string
  // type of the peer entity on the other end of the edge. Required for
  // the unified PATCH builder to emit JSON:API §9 resource identifiers
  // without consulting the schema. Backend started emitting this in
  // TKT-ZEKO4; older servers omit it.
  type: string
  direction?: 'outgoing' | 'incoming'
  meta?: Record<string, unknown>
  // Plumbing-only — no widget exposes per-edge body editing yet, but
  // the wire shape carries it so a future ticket can wire UI without
  // touching types again.
  content?: string
}

export interface ListResponse<T> {
  data: T[]
  meta: ListMeta
  included?: Record<string, T>
  // Collection-scope verb verdicts (phase 1: just `create`). Same
  // semantics as Entity._actions: absent = anonymous/pre-rollout
  // fallback; empty {} = all denied.
  _actions?: Record<string, boolean>
}

export interface ListMeta {
  total: number
  page: number
  per_page: number
  has_more: boolean
  next_cursor?: string
}

export interface ListParams {
  page?: number
  per_page?: number
  cursor?: string
  sort?: string
  fields?: string
  include?: string
  // world selects which FACE of each entity is served, and which entities
  // appear at all — an entity with no face in the requested world is omitted
  // entirely, because existence in a world IS the publication bit.
  //
  // Absent means the default world. Note the backend refuses `world` combined
  // with `q` (422), and honors it only on `/{plural}`, `/{plural}/{id}`,
  // `/_views/{type}/{id}` and `/_history/{type}/{id}` — see
  // internal/dataentry/world.go's worldCapablePath.
  world?: string
  // list_id names which configured list this read is serving, so the server
  // can apply that list's `condition:` — the predicate expression that states
  // membership rules `filters:` cannot (`or`, grouping, date arithmetic).
  //
  // It carries an ID, never the expression: the condition is operator config
  // the server already holds, so a caller can only select among lists it was
  // already free to name. Omitting it is not an error — the server then
  // applies no condition and returns the ACL-scoped superset.
  list_id?: string
  /**
   * query_scope selects which rows are MEMBERS of the result, from the entity
   * type's declared `query_scopes:`. Orthogonal to `world`, which selects
   * which FACE of an already-included entity is served: a world never changes
   * the row count, a scope is exactly a change to the row count.
   *
   * Omitted (not empty) when the view names no scope, so the entity type's
   * `default` applies. An empty string would be a name that resolves to
   * nothing, which the server answers with a 400.
   */
  query_scope?: string
  [key: `filter[${string}]`]: string | undefined
}

// Side panel types
export interface SidePanelField {
  label: string
  values?: string[]
  propType?: string
}

export interface SidePanelEntity {
  id: string
  title: string
  type: string
  editFormId?: string
  fields?: SidePanelField[]
  content?: string
  hasContent: boolean
}

// Add target for side panel (reusable)
export interface SidePanelAddTarget {
  entityType: string
  formId: string
  label: string
}

// Add button info for side panel
export interface SidePanelAddInfo {
  relation: string
  linkAs: 'from' | 'to'
  peerId: string
  targets: SidePanelAddTarget[]
}

// Link existing button info for side panel
export interface SidePanelLinkInfo {
  relation: string
  linkAs: 'from' | 'to'
  peerId: string
  entityTypes: string[]
}

export interface SidePanelSection {
  heading: string
  sectionId: string
  display: 'cards' | 'list' | 'properties'
  isEmpty: boolean
  emptyMessage?: string
  fields?: SidePanelField[]
  entities?: SidePanelEntity[]
  addInfo?: SidePanelAddInfo
  linkInfo?: SidePanelLinkInfo
}
