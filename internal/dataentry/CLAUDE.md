# dataentry — rules for new code

The data-entry web app (Go API + Vue 3 SPA in `frontend/`). Two rule sets
apply here: the write-validation policy, and the `_actions` affordance
contract.

## Validation policy for write APIs

rela's storage is permissive: markdown + YAML frontmatter, edited freely by
external tools alongside the API. Philosophy: **tolerate temporarily invalid
data**; the `analyze_*` tools surface inconsistencies the storage layer
doesn't reject.

Write-time checks split into three classes (DEC-HWZHA):

| Class | When | HTTP |
|---|---|---|
| **Hard 400 — malformed wire format** | Request structure broken, detectable without the metamodel | 400 |
| **Hard 422 — structural impossibility** | Storage layer literally cannot persist this | 422 |
| **Write-with-warnings** | Soft conditions: target type mismatch, missing target, unknown/required-unset/mistyped meta keys | 200 |

The 200 path performs the write and returns warnings `{code, path, detail}`
in the body — `code` matches the corresponding `analyze_*` finding code so
UIs de-duplicate against analyze runs.

**Resist drift toward hard rejection on soft conditions.** Before adding a
422 on a write path, ask: *could a hand-editor produce this state in a
markdown file?* If yes, it's soft — warn, don't reject. JSON:API-style
"validate-then-422" assumes wire and storage share a closed schema; rela's
storage is intentionally more permissive.

## Action affordances (`_actions`)

Every entity and list response carries `_actions: map[string]bool`. The SPA
reads it to decide which write controls to render. The map is a **UI hint** —
the server re-authorizes every write.

Rules for new write code here:

- **Route every `acl.WriteRequest{Op:...}` through `translateVerb`** in
  `affordances.go`. A grep test (`lint_test.go`) enforces it: no other file
  in this package may construct the literal. The shared constructor is the
  structural guarantee that the affordance map and the actual write resolve
  to the same ACL request.
- **Don't trust `_actions` for authorization.** The write endpoint must
  re-authorize. `affordances_contract_test.go` pins the invariant: every
  `_actions[v] == false` ⇒ 403 on the write, every `true` ⇒ 2xx.
- **New verbs require coordinated changes:** add an `acl.Op` constant, a
  `translateVerb` case, and update `docs/data-entry/api-reference.md`. Old
  SPAs ignore unknown keys; removing/renaming a verb is a major API bump.
- **Phase 1 verbs:** `create` (per-collection), `update`/`delete`/`rename`
  (per-item). `transition:*` and `relation:*` are deferred until ACL gains
  Op variants or extension fields.
- **`action:<id>` keys are detail-page actions (TKT-VVS16W), not verbs.** They
  ride per-entity responses only and are emitted ONLY as `true`: absence means
  "not offered", the opposite of a verb's render-when-absent default, because
  the set is open and only the server can enumerate it. The key and the
  `POST /_action/{id}` gate call one function,
  `detailActionCheck.Allows`; keep it that way.
  `TestDetailAction_AffordanceAndGateAgree` pins that a missing key is a
  refused POST and a present one runs. `when:` is judged on the REDACTED
  entity; judging the raw one turns the button into an oracle on a hidden
  field.

Rules for new write affordances in the Vue SPA (`frontend/`):

- **Gate every entity-CRUD button** on `entity._actions?.[verb] !== false`
  (or `listResponse._actions?.create !== false` for collection verbs). False
  → hide; anything else (true/undefined/absent) → render. Absent is the
  defensive-render fallback for non-data-entry callers; the server still 403s.
- **No `useACL()` composable or client-side ACL evaluator.** TKT-AWM6L's
  wont-fix rejected this. The SPA reads booleans the server computed — no
  computation, merging, or prediction.
- **Adding a write affordance** requires (a) a backend `translateVerb` entry
  plus a `perItemVerbs`/`perCollectionVerbs` update, and (b) the inline
  `v-if` on the component. No ESLint enforcement; code review catches drift.

## The entity-detail view is read-only BY DEFAULT, not absolutely

TKT-651W stripped `+ Add` / `Link Existing` from entity-detail view sections
because "editing the graph from inside a read view blurs the line between viewing
and editing". TKT-R4BMJM **narrowed** that to "read-only unless a section opts
in", and did not reverse it. Rules for new code:

- **Absence is the read-only default, and it is load-bearing.** A section with no
  `create:` block emits nothing on the wire.
  `TestV1Views_NoAddOrLinkInfoOnSections` asserts the absence of `addInfo`,
  `linkInfo` **and** `create` across every section shape; it narrowed rather than
  inverted, so all five of its original cases still assert absence. A change that
  made the affordance default-on fails there.
- **`v1.ViewAddInfo` is still side-panel-only.** Its doc comment forbids reuse
  from a view response, and RR-R8X6 predicted that the misleading `View` prefix
  would tempt exactly that — which is what happened during TKT-R4BMJM's planning.
  The view path uses `v1.ViewSectionCreate`, a separate type.
- **Link-existing did NOT come along.** `resolveSectionButtonsWithTraverse` builds
  `LinkInfo` unconditionally, with no form check and no permission check, which is
  why the view path has its own `resolveSectionCreate` rather than reusing it.
  Don't "simplify" the two back into one without gating LinkInfo first.
- **Both add affordances are ACL-gated through `creatableTargets`.** A type is
  offered only when a form resolves for it AND `computeCollectionActions` says the
  principal may create it. Before TKT-R4BMJM the side panel offered a button
  whenever a form existed, with no principal involved.

## Documents (`documents:` + `/_documents/...`)

Two kinds, discriminated by `DocumentConfig.IsStandalone()` (empty
`entity_type:`). Rules for new code:

- **Never let one URL shape serve the other kind.** `/_documents/{name}/{id}`
  is entity-anchored, `/_documents/{name}` is standalone; each 400s on the
  other. Do not "helpfully" fall back to rendering with an empty or guessed
  entry id — that produces a document about the wrong thing, silently.
- **`permission:` is an intent gate, NOT the confidentiality boundary —
  UNLESS the document is elevated.** Document content is bounded by
  `lua.ReadDeps.VisibleReader` like every other read path, so a principal who
  cannot read the underlying entities renders an empty report regardless. That
  is why documents are ungated by default
  (`TestStandaloneDocument_UngatedByDefault` pins it) — don't "harden" it into
  a required field. What it buys on an ordinary document is that a report
  *claiming* a scope its reader cannot compute is withheld rather than served
  as a smaller number that looks authoritative: misinformation, not disclosure.
- **An elevated document (`allow_acl_bypass: read`) inverts that, and gets a
  SECOND gate (TKT-Y3JVFK).** Its Lua reads through a raw handle, so nothing
  downstream bounds the output and `permission:` becomes the whole boundary —
  hence required at config load, and enforced by `authorizeElevatedDocument`, a
  closed switch on the ACL IMPLEMENTATION. It must not be folded into
  `gateDocumentPermission`: that consults the read gate, which returns
  `nopReadGate` under NopACL *and* ReadOnlyACL with `HoldsPermission` ⇒ true, so
  a read-gate predicate FAILS OPEN (the RR-CWWJGW shape, same as the nav filter
  below). NopACL **denies** here, diverging from `authorizeCommand` — an
  elevated document has no pre-ACL behavior to preserve, so granting would only
  make the boundary inert while looking configured.
- **Only `read` is accepted on a document; `write`/`read+write` are a config
  error.** A render is served on a GET, so elevated writes there would break
  idempotence under prefetch/refresh/retry and foreclose the caching an
  elevated, principal-independent render is uniquely suited to (TKT-OGR566,
  RR-P4E9GL). Elevated writes belong in an automation action or a schedule.
- **This does NOT mean a render cannot mutate — it can, today.** Documents
  render on a `WriterRuntime` and `registerBindings` has no `isDocument` guard,
  so ordinary `rela.create_entity`/`update_entity`/`delete_entity`/`write_file`
  are callable in a document script, bounded by the caller's ACL (pre-existing;
  TKT-PX5YL7). Withholding the elevated Mutator narrows *elevation*; it does not
  make the surface read-only. Don't write "a render cannot write" in a comment
  or doc — say "cannot write past the ACL", which is what is true.
- **The elevated script is TRUSTED CODE (RR-LWD8N3, accepted).** Nothing stops
  it printing what it reads, so `permission:` grants "may read whatever this
  script reads", not "may view this report". Review of the bypass block is the
  mitigation; say so in docs rather than implying enforcement.
- **Gate before the renderer anyway.** Content would be safe either way, but a
  denied caller must not be able to trigger an expensive Lua aggregation.
- **Deny with a 403 that NAMES the document and permission.** Do not disguise
  it as the unknown-document 404: which documents exist is not a secret (they
  are `data-entry.yaml` keys — see "The configuration is not a secret; the data
  is" in the root CLAUDE.md), and an opaque denial is unsupportable at scale.
  The uniform 404 is for **entity ids**, where existence genuinely is secret.
- **Do NOT filter `/_config` per principal.** It serves the navigation tree
  verbatim ("The configuration is not a secret; the data is"); an earlier draft
  of TKT-M1AX6P filtered it and that was reverted — don't reintroduce it. The
  *sidebar* and *dashboard* are a different matter: TKT-TXDK8U added an opt-in
  `permission:` filter on nav entries and TKT-53KICM the same on dashboard
  cards, each served by its own per-principal endpoint (see the section below).
  A document with no `permission:` is still listed for everyone and 403s on
  render.
- **A standalone render has no entry entity, so it has no `ContentHash`,
  `Entities`, or disk cache**, and it is script-only. Don't reintroduce those
  by keying a cache on something else; there is nothing to invalidate against
  (TKT-E1FO1 is the real fix).

## Presentation filtering (`permission:` on a nav entry or dashboard card)

`permitsGatedUIElement` (`views_handler.go`) is the ONE policy switch behind
both surfaces: `permitsNavEntry` wraps it to omit entries from `/_sidebar`
(TKT-TXDK8U), and `handleV1Dashboard` calls it to omit cards from
`/_dashboard` (TKT-53KICM). Rules for new code:

- **It is a UX filter, not a boundary.** Nothing downstream may rely on an
  element being hidden. The target enforces what it always did — a hidden
  card's query still returns its ACL-scoped rows through `_search`.
  `TestNavPermission_FilterIsPresentationOnly` and
  `TestDashboardPermission_FilterIsPresentationOnly` assert precisely that, in
  both directions. Same rule as `_actions` above.
- **Do NOT filter `/_config`.** It serves the navigation tree AND the
  `dashboard:` block verbatim, on purpose (root CLAUDE.md, "The configuration
  is not a secret; the data is"). Filtering it would be concealment-shaped, and
  an earlier attempt at exactly that was reverted in TKT-M1AX6P.
  `TestNavPermission_ConfigUnfiltered` and
  `TestDashboardPermission_ConfigUnfiltered` pin it. This is *why* the
  dashboard needed its own endpoint rather than a filtered `_config`.
- **The ReadOnlyACL arm is load-bearing.** `readGateFromContext` returns
  `nopReadGate` under ReadOnlyACL *and* NopACL, and its `HoldsPermission`
  returns true — so a predicate written against the read gate alone fails OPEN
  under `--read-only`. That is the RR-CWWJGW shape. Keep the explicit arm ahead
  of the gate; `TestNavPermission_ReadOnlyArmIsExplicit` and
  `TestDashboardPermission_ReadOnlyArmIsExplicit` are the canaries (each
  attaches a deny-everything gate, so removing the arm fails them). Match value
  **and** pointer forms — `AuthorizeWrite` has a value receiver.
- **The switch stays closed.** A new `acl.ACL` implementation hides gated
  elements until someone adds an arm; the failure mode of forgetting should be
  a missing menu item, not an unintended one.
- **Keep the grep guard's allow-lists short.** `TestNavFilterStaysPresentational`
  greps two needles: `permitsNavEntry(` (one file) and `permitsGatedUIElement(`
  (two). Adding a caller means widening a list, which is a deliberate argued
  exception — not routine. If you need an authorization check, build one; see
  `authorizeCommand`.
- **A third gated surface shares the predicate, it does not copy it.** Copying
  reintroduces the chance to get the read-only arm wrong, which was RR-XYO03L
  (critical): the copy denied under ReadOnlyACL and so hid entries from
  *everyone* based on a process-wide flag about writes.

## Custom apps (`apps/<id>/` + `_apps/{id}/...` + the bridge)

User-authored apps served in a sandboxed iframe. An app is a **folder**
`apps/<id>/` with an `index.html` (plus sibling assets). Rules for new code:

- **Apps are folder-discovered on disk**, served per-entry via `os.OpenRoot`
  (see `apps.go`). No `apps:` config — `scanApps` lists `apps/<dir>/` that have
  an `index.html`; id = folder name; metadata from `<meta name="rela-app:*">` in
  index.html. Unpublish by renaming the folder or removing index.html. Never
  store apps in the entity store — filesystem in every backend, like
  `actions/`/`templates/`. The scan + per-entry reads are per-request (no
  watcher wiring); fine for a handful of apps.
- **Apps MUST declare `<meta name="rela-app:bridge-version" content="N">`.**
  `currentBridgeVersion` (apps.go) is the contract this server serves;
  `validateBridgeVersion` rejects a missing/unparseable version and one NEWER
  than the server (scanApps drops it from the listing; handleV1App 422s the
  index serve). This is the forward-compat seam: on a breaking bridge change,
  bump `currentBridgeVersion` and add a per-version path (e.g. a version-matched
  `_rela.js`) keyed on the app's declared version — don't break old apps. Older
  (<= current) versions stay allowed.
- **The app loads same-origin from `/api/v1/_apps/<id>/`** (iframe `src=`, not
  `srcdoc`) so its sibling files resolve. It is therefore same-origin with the
  API — **the path-scoped CSP header is the whole boundary**, not origin-`null`.
- **The CSP is a path-scoped HEADER, not a `<meta>`** (`appCSP(base)` in
  `apps_handler.go`). Every resource directive is scoped to the app's own
  subpath (`script-src /api/v1/_apps/<id>/ …`), NOT `'self'` (which would
  include `/api/`, letting `<img src=/api/v1/tickets/x>` exfiltrate).
  `connect-src 'none'` blocks the app's own fetch/XHR/WS, so the
  `MessageChannel` bridge (not a network request) is the only path to the API.
  `form-action 'none'` + sandbox block form/nav exfil. CSP correctness is now
  the entire security boundary — keep the path-scoping exact and tested. Do NOT
  reintroduce a `<meta>` CSP or add an author-controlled egress allow-list
  (self-defeating).
- **The bridge SDK is served, not injected.** `appSDKSource()` is served at the
  reserved `/_apps/<id>/_rela.js`; apps include `<script src="_rela.js">`. The
  app cannot shadow `_rela.js` or serve any `_`-prefixed entry from its files.
  No server-side HTML rewriting of the app's index.
- **Readiness is replayable; prefer `rela.ready` / `rela.whenReady(cb)` over the
  `rela:ready` event.** The handshake can complete before an app's inline code
  runs (e.g. a large `<script src="_rela-editor.js">` between `_rela.js` and the
  app's listener delays it past the port arrival) — an `addEventListener('rela:ready')`
  added that late never fires. The SDK resolves a `rela.ready` Promise on the
  handshake (and `rela.whenReady(cb)`), which is not-missable. The event is kept
  for back-compat only. `TestAppSDKReadiness` pins this. Don't add `ready`/
  `whenReady`/`isReady` to `appSDKMethods` — they're local SDK helpers, not host
  calls.
- **Optional markdown editor: `<rela-editor>` from the reserved
  `/_apps/<id>/_rela-editor.js`** (`appEditorSource()`), with its stylesheet at
  `/_apps/<id>/_rela-editor.css` (`appEditorCSSSource()`). A FILE, not an inline
  `<style>`: the app CSP has no `'unsafe-inline'`, so an injected style element
  is blocked outright and the editor renders completely unstyled. Both are
  embedded from `app_editor_dist/` — a **build artifact** produced by
  `frontend/vite.editor.config.ts` (`npm run build` runs it), gitignored like
  `static/v2`, with a committed `.gitkeep` so the glob embed compiles on a clean
  checkout; `TestAppEditorBundleEmbedded` skips when unbuilt and asserts the
  contract when built. Separate from `_rela.js` so only apps that opt in pay the
  bundle.

  **The element's public contract is the swap seam — keep it minimal**: property
  `value` (whitespace-exact), attributes `placeholder`/`readonly`, native
  `input`/`change` events, `focus()`. Everything else (the editor underneath,
  the toolbar, the generated DOM) is unsupported. That is not theoretical: the
  editor was swapped from EasyMDE to Milkdown (TKT-D2JML7) with the contract
  unchanged. Light DOM, not shadow DOM — ProseMirror works in a shadow root, but
  the app's own `_rela.css` tokens would not reach it and the editor is meant to
  look like the app it sits in. Programmatic `.value` sets are silent (no
  `input`), matching a native `<textarea>`.

  **There are only TWO editor assets.** The EasyMDE build served a third, a Font
  Awesome woff2 at `_rela-editor.woff2`, with an `Access-Control-Allow-Origin: *`
  exception because the sandboxed iframe is null-origin. Milkdown draws its
  toolbar with inline SVG, so the asset, the reserved path and that CORS
  exception are all gone; a test pins the path as a 404 so they cannot creep
  back in together.

  **The CSP's `style-src` does NOT block ProseMirror.** It governs stylesheets
  and the `style` ATTRIBUTE; ProseMirror positions its chrome by writing DOM
  style PROPERTIES (`el.style.left`), which is the CSSOM and is permitted. This
  was verified against a server replicating `appCSP` exactly before the port —
  an earlier note here recorded the opposite and was wrong.
- **Optional styling is served at the reserved `/_apps/<id>/_rela.css`**
  (`appCSSSource()` = theme tokens + the atomic `.btn`/`.input`/`.card`). The
  tokens are embedded from `apps_tokens.css`, a **byte-identical copy** of
  `frontend/src/styles/tokens.css` (the SPA's source of truth) —
  `TestAppTokensCSSInSyncWithFrontend` fails on drift; re-copy, don't hand-edit.
  Keep `_rela.css` to tokens + *atomic, pure-presentation* controls; component-
  shaped classes (tables, selects, modals) stay out (they'd become an
  unmaintainable frozen contract). Theme follows the host: the SDK toggles
  `dark` on the app's `<html>` from the `rela:theme`/handshake messages, using
  the same `:root.dark` selector as the SPA.
- **No app-specific ACL.** An app inherits the user's permissions: every
  `/api/v1/*` call runs under the user's session (reads → `readGate`, writes →
  `entitymanager`). Do **not** add an `OpRunApp`/`AppSubject` — it would break
  `--read-only` mode (`ReadOnlyACL` denies all `WriteRequest`s) and panic on the
  sealed-`Subject` switch. The app *is* the user. (The bridge is the seam where
  a future per-app *restriction* — e.g. read-only — would be enforced.)
- **The host bridge is a closed method allow-list, never a URL proxy.**
  `frontend/src/bridge/relaBridge.ts` maps each allowed method to one existing
  api-client call. Adding a capability = adding a named method, never a generic
  "fetch this path". Keep `appSDKMethods` (Go, in `apps_sdk.go`) in sync with
  `BRIDGE_METHODS` (the dispatcher).

## Operator customisation (`custom/` + `/_custom/`)

The operator's `custom/` project directory, served at `/_custom/<path>`.
`custom.css` / `custom.js` there are referenced from the SPA shell; every other
file (fonts, logos, images) is served as-is. Rules for new code:

- **The SPA shell rewrite is the ONE server-side HTML rewrite in this codebase,
  and it is deliberately scoped to rela's own shell.** The `apps/` rule below
  ("served, not injected") still stands and is not weakened by it. The reason
  the two differ is a *security boundary*, not ownership: an app's path-scoped
  CSP **is** the entire boundary confining an untrusted installable app, so
  injecting script into an app's index would require widening that CSP and
  puncture it. The SPA shell has no CSP at all, so the rewrite crosses no
  boundary. **TRIP-WIRE: if a CSP is ever added to the SPA route, that
  reasoning lapses and the injection must be revisited.**
- **Splice, don't parse.** `injectTags` does a targeted string insertion before
  `</head>` / `</body>`. Do NOT swap in a `golang.org/x/net/html` parse+render
  round-trip: it normalises the whole document (attribute quoting/order, entity
  re-encoding) and would be the first `html.Render` in `internal/`. The shell is
  our own embedded, known-shape output. x/net/html already being a dependency is
  not a reason to use it.
- **Four shells precomputed, existence re-checked per request.** Precomputing
  removes the cache-population race; the per-request stat means adding
  `custom.css` needs no restart. A file that is *served* at `/_custom/` but
  never *referenced* in the shell is the confusing half-state this avoids.
- **The containment boundary is `validCustomEntry` + a NESTED `os.OpenRoot`.**
  TKT-IWMETE replaced the old two-name allowlist (which made traversal
  impossible before touching the filesystem) with a served directory, so path
  validation is now the primary boundary, not defence-in-depth.
  **The nested root is security-critical**: a symlink inside `custom/` pointing
  at `../metamodel.yaml` never leaves the project root, so a single
  `os.OpenRoot(projectRoot)` would follow it. Only the second root scoped to
  `custom/` refuses it — don't "simplify" it away
  (`TestOpenCustomEntry_SymlinkInsideProject` pins this).
- **Traversal is NEUTRALISED, not rejected.** `path.Clean` anchors
  `../secret.txt` to `custom/secret.txt`, which may legitimately exist. Assert
  *containment* (no file outside `custom/` is ever served), never "the request
  errors" — a rejection-shaped test passes against a leaky implementation.
- **The dot-segment rule is for operator accidents, not traversal.** It refuses
  `.env`, `.git/config`, `.DS_Store`. It contributes ZERO traversal defence
  (`path.Clean` resolves `..` before it runs) and is a filename heuristic, not a
  secrets scanner — `notes.md` and `custom.css~` are served.
- **Assets are delivered by `http.ServeContent`, not `w.Write`.** That is what
  gives conditional requests, `Range` and correct `HEAD` in one call; before it,
  a static webfont re-transferred on every navigation and a `HEAD` returned a
  body. Two things must survive any change here: the explicit `Content-Type`
  (ServeContent sniffs when it cannot infer one — the exact behaviour `nosniff`
  exists to prevent) and the pre-read size gate, since ServeContent would
  otherwise happily stream a file of any size.
- **The ETag is modtime+size, deliberately not a content hash.** Hashing would
  read the whole file per request — a read amplifier on an unauthenticated
  route, and the cost ServeContent was adopted to avoid. `http.FileServer` makes
  the same trade. The blind spot (an edit preserving both modtime and size) is
  bounded by `Cache-Control: no-cache`, which forces revalidation anyway.
- **`openCustomEntryFile` duplicates the containment chain** because it must
  return a live handle, so it cannot delegate to `openCustomEntry`. A duplicated
  security check drifts: `TestOpenCustomEntryFile_MatchesOpenCustomEntry` pins
  that the two agree, and `TestOpenCustomEntryFile_NeverEscapes` attacks the new
  path independently.
- **`/_custom/` is PUBLIC and UNAUTHENTICATED** — not an `isAPIPath`, so outside
  both the JWT gate and ACL, deliberately, so the shell loads before login. This
  is a wider exposure than `apps/`, which this code otherwise copies. Every
  failure collapses to one 404.
- **`custom.js` is FULLY TRUSTED — the opposite posture from `apps/`.** It runs
  same-origin in the SPA's document with no CSP, no sandbox, and unrestricted
  API access. That is correct (the operator already controls metamodel, Lua and
  ACL), but never reason "apps are sandboxed, therefore this is too."
- **Token CSS must never be layered.** See `frontend/CLAUDE.md`;
  `TestTokensCSSNeverLayered` pins both copies.
