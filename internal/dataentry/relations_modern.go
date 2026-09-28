package dataentry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"

	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// Warning is a type alias for entity.Warning so that handlers
// in this package can write `dataentry.Warning` without importing the
// entitymanager package at every call site. Behavior is identical.
type Warning = entity.Warning

// validateRelationsModern runs the validation phase of the modern
// reconciler without performing any writes. It returns:
//
//   - warnings: soft-condition findings (DEC-HWZHA write-with-warnings).
//     Edges flagged by warnings will still be written by applyRelationsModern.
//   - err: a hard 422 (structural impossibility) or 400 (caller bug). On err
//     the caller MUST NOT proceed to the entity-update or write phases.
//
// This split lets handleV1UpdateEntity validate relations BEFORE the
// entity is updated, so a structural relation problem doesn't leave
// the entity half-written.
func (h *writeHandler) validateRelationsModern(
	ctx context.Context, entityID string, pathEntityType string, desired map[string]v1.RelationsUpdate,
) ([]Warning, error) {
	if len(desired) == 0 {
		return nil, nil
	}
	meta := h.schema().Meta
	var warnings []Warning

	// Self-loop shape_conflict detection: a body that references the
	// path entity under BOTH a canonical name AND its inverse for the
	// same canonical relation refers to the same physical edge twice.
	// Skipped for symmetric relations (whose inverse equals canonical
	// and which have no preferred direction).
	if err := detectSelfLoopShapeConflict(meta, entityID, desired); err != nil {
		return nil, err
	}

	for bodyKey, upd := range desired {
		if !upd.DataPresent {
			return nil, &v1.WireError{
				Code:   "data_required",
				Path:   "/relations/" + v1.JSONPointerEscape(bodyKey) + "/data",
				Detail: "`data` field is required when a relation wrapper appears",
			}
		}

		canonical, incoming, ok := resolveDirection(meta, bodyKey)
		if !ok {
			return nil, &structuralError{
				Code:   "unknown_relation_type",
				Path:   "/relations/" + v1.JSONPointerEscape(bodyKey),
				Detail: fmt.Sprintf("relation type %q is not defined in the metamodel", bodyKey),
			}
		}
		relDef := meta.Relations[canonical]

		// Path-entity-side type allowlist:
		//   - outgoing: path entity is source, check relDef.From
		//   - incoming: path entity is target, check relDef.To
		allowedPathTypes := relDef.From
		warningCode := "source_type_not_allowed"
		if incoming {
			allowedPathTypes = relDef.To
			warningCode = "target_type_not_allowed"
		}
		if !containsString(allowedPathTypes, pathEntityType) {
			warnings = append(warnings, Warning{
				Code:      warningCode,
				Path:      "/relations/" + v1.JSONPointerEscape(bodyKey),
				Detail:    fmt.Sprintf("relation %q does not declare %q as an allowed %s type", canonical, pathEntityType, sideLabel(incoming, true)),
				Direction: directionLabel(incoming),
			})
		}

		for i, ref := range upd.Data {
			edgePath := fmt.Sprintf("/relations/%s/data/%d", v1.JSONPointerEscape(bodyKey), i)

			// Content on a non-content-bearing relation type is a
			// structural impossibility — the file format can't hold
			// a body for that type.
			if ref.Content != nil && !relDef.Content {
				return nil, &structuralError{
					Code:   "content_not_supported",
					Path:   edgePath + "/content",
					Detail: fmt.Sprintf("relation type %q does not support per-edge content", canonical),
				}
			}

			// Wire-format violation for managed order properties: only
			// finite numeric values are acceptable on _order_out / _order_in
			// because the engine's sort/midpoint math depends on them.
			if err := validateManagedOrderMeta(canonical, ref, edgePath, &relDef); err != nil {
				return nil, err
			}

			// Soft conditions surfaced as warnings. The peer is whichever
			// side the path entity is NOT on.
			ws := h.collectEdgeWarnings(ctx, canonical, &relDef, ref, edgePath, incoming)
			warnings = append(warnings, ws...)
		}
	}
	return warnings, nil
}

// directionLabel returns the JSON-friendly string for the direction
// flag. Matches the Warning.Direction field contract from TKT-GFQK.
func directionLabel(incoming bool) string {
	if incoming {
		return "incoming"
	}
	return "outgoing"
}

// sideLabel produces a human-readable side description for warning
// messages ("source" / "target"). pathSide=true asks for the side the
// path entity is on; pathSide=false asks for the peer side.
func sideLabel(incoming, pathSide bool) string {
	if incoming == pathSide {
		return "target"
	}
	return "source"
}

// collectEdgeWarnings runs the soft-condition checks for a single
// resource identifier. None of these block the write.
//
// `incoming` flips the side of the type-allowlist checks: when true,
// the `ref` is the SOURCE side of the canonical edge (the path entity
// is the target). Warning codes stay the same so client de-dup by
// code keeps working; the `Direction` field disambiguates.
func (h *writeHandler) collectEdgeWarnings(
	ctx context.Context, relType string, relDef *metamodel.RelationDef,
	ref v1.ResourceIdentifier, edgePath string, incoming bool,
) []Warning {
	var warnings []Warning
	meta := h.schema().Meta
	direction := directionLabel(incoming)

	peer, err := h.store.GetEntity(ctx, ref.ID)
	if err != nil {
		warnings = append(warnings, Warning{
			Code:      "target_not_found",
			Path:      edgePath + "/id",
			Detail:    fmt.Sprintf("peer entity %q does not exist; the edge will be created but reference a missing peer", ref.ID),
			Direction: direction,
		})
	} else {
		if peer.Type != ref.Type {
			warnings = append(warnings, Warning{
				Code:      "target_type_mismatch",
				Path:      edgePath + "/type",
				Detail:    fmt.Sprintf("expected peer type %q, but %q is of type %q", ref.Type, ref.ID, peer.Type),
				Direction: direction,
			})
		}
		// Peer-side type allowlist: when path entity is source
		// (outgoing), the peer is target and must be in relDef.To;
		// when path entity is target (incoming), the peer is source
		// and must be in relDef.From.
		peerSideAllowed := relDef.To
		if incoming {
			peerSideAllowed = relDef.From
		}
		if !containsString(peerSideAllowed, peer.Type) {
			warnings = append(warnings, Warning{
				Code:      "target_type_not_allowed",
				Path:      edgePath,
				Detail:    fmt.Sprintf("relation %q does not declare %q as an allowed %s type", relType, peer.Type, sideLabel(incoming, false)),
				Direction: direction,
			})
		}
	}

	// Managed order properties are recognized when the relation type is
	// orderable on the corresponding side. They are not declared in
	// relDef.Properties — the engine reserves them.
	isManagedOrderProperty := func(name string) bool {
		return (name == metamodel.OrderPropertyOut && relDef.OutgoingOrderProperty() != "") ||
			(name == metamodel.OrderPropertyIn && relDef.IncomingOrderProperty() != "")
	}

	// Closed-schema check on meta keys.
	for k := range ref.Meta {
		if isManagedOrderProperty(k) {
			continue
		}
		if _, known := relDef.Properties[k]; !known {
			warnings = append(warnings, Warning{
				Code:      "unknown_meta_key",
				Path:      edgePath + "/meta/" + v1.JSONPointerEscape(k),
				Detail:    fmt.Sprintf("relation type %q does not declare meta property %q", relType, k),
				Direction: direction,
			})
		}
	}
	for _, k := range ref.MetaUnset {
		if isManagedOrderProperty(k) {
			continue
		}
		if _, known := relDef.Properties[k]; !known {
			warnings = append(warnings, Warning{
				Code:      "unknown_meta_key",
				Path:      edgePath + "/meta_unset",
				Detail:    fmt.Sprintf("relation type %q does not declare meta property %q", relType, k),
				Direction: direction,
			})
		}
	}

	// Per-property type validation for declared keys with provided values.
	for k, v := range ref.Meta {
		propDef, known := relDef.Properties[k]
		if !known {
			continue // already warned above
		}
		if err := meta.ValidatePropertyValue(k, &propDef, v); err != nil {
			warnings = append(warnings, Warning{
				Code:      "meta_type_mismatch",
				Path:      edgePath + "/meta/" + v1.JSONPointerEscape(k),
				Detail:    err.Error(),
				Direction: direction,
			})
		}
	}

	return warnings
}

// applyRelationsModern performs the diff and write phase of the modern
// reconciler. Validation should have run already via
// validateRelationsModern; this function does NOT re-validate. It only
// surfaces additional warnings that depend on post-merge state (e.g.
// required-meta-unset) and store-write errors.
//
// Edges flagged by the validation phase as "target missing" or
// "target type mismatch" are written directly through the store
// rather than the EntityManager — the EntityManager's CreateRelation
// rejects writes whose target doesn't exist, but DEC-HWZHA's policy is
// to permit the write with a warning. The store does not check target
// existence, so the direct write succeeds and `analyze_*` flags it on
// the next run.
//
// Returns warnings collected during the apply phase plus any error
// that prevented further writes. On a write-loop error, the relations
// already written stay written — the caller treats this as the
// documented atomicity gap.
//
//nolint:gocognit // diffs desired vs. existing relation sets and issues add/remove ops per peer; the branches are the set-reconciliation cases, not shared logic to extract.
func (h *writeHandler) applyRelationsModern(
	ctx context.Context, addr entityRef, desired map[string]v1.RelationsUpdate,
) ([]Warning, error) {
	entityID := addr.ID
	if len(desired) == 0 {
		return nil, nil
	}
	meta := h.schema().Meta
	em := h.manager
	var warnings []Warning

	for bodyKey, upd := range desired {
		canonical, incoming, ok := resolveDirection(meta, bodyKey)
		if !ok {
			// validateRelationsModern already screened this; defensive.
			return warnings, &structuralError{
				Code:   "unknown_relation_type",
				Path:   "/relations/" + v1.JSONPointerEscape(bodyKey),
				Detail: fmt.Sprintf("relation type %q is not defined in the metamodel", bodyKey),
			}
		}
		relDef := meta.Relations[canonical]
		direction := directionLabel(incoming)

		// The tail a NEW edge of this type is created at (BUG-64MU2Q).
		// Non-zero only when the addressed entity is the tail AND the type
		// is content-scoped:
		//
		//   - an INCOMING edge tails at the PEER, and the peer's face is not
		//     this request's to choose;
		//   - an identity-scoped edge belongs to the entity, so every face
		//     shares it and the tail is the zero face by definition.
		//
		// `addr`, not `ref`: the per-edge loops below bind `ref` to a
		// v1.ResourceIdentifier, which has no face.
		//
		// This is the CREATE tail only. An existing edge carries its own —
		// see tailOf below.
		newTail := entity.Face("")
		if !incoming && relDef.Scope.IsContent() {
			newTail = addr.Face
		}

		// Dedup by ID. Duplicate resource identifiers in the body
		// collapse to a single edge — matches the legacy IDs-only
		// reconciler's set semantics and is what callers expect when
		// e.g. a picker re-emits the same selection twice.
		desiredByID := make(map[string]v1.ResourceIdentifier, len(upd.Data))
		desiredOrder := make([]string, 0, len(upd.Data))
		for _, ref := range upd.Data {
			if _, dup := desiredByID[ref.ID]; !dup {
				desiredOrder = append(desiredOrder, ref.ID)
			}
			desiredByID[ref.ID] = ref
		}

		current := h.currentEdgesByPeer(ctx, entityID, newTail, canonical, incoming)

		// The tail an EXISTING edge is addressed by. Read off the edge
		// itself, never recomputed from the request: the store's *State
		// methods treat the tail as identity, so an address derived from
		// the request modifies a different edge — or, on the incoming path
		// where the tail belongs to the peer, none at all while reporting
		// the write as attempted.
		tailOf := func(e *entity.Relation) entity.Face {
			if e == nil {
				return newTail
			}
			return e.FromFace
		}

		// Adds and upserts.
		for _, id := range desiredOrder {
			ref := desiredByID[id]
			finalProps, finalContent, contentSet := mergeEdgeMeta(current[ref.ID], ref)

			ws := requiredMetaWarnings(canonical, &relDef, ref, finalProps,
				fmt.Sprintf("/relations/%s/data", v1.JSONPointerEscape(bodyKey)), direction)
			warnings = append(warnings, ws...)

			from, to := edgeEndpoints(entityID, ref.ID, incoming)

			existing, exists := current[ref.ID]
			if exists && isEdgeNoOp(existing, finalProps, finalContent, contentSet, ref) {
				continue // value-based no-op suppression
			}
			if err := h.upsertEdge(ctx, edgeWrite{
				from: from, to: to, relType: canonical, ref: ref,
				exists: exists, existingTail: tailOf(existing), newTail: newTail,
				props: finalProps, content: finalContent,
			}); err != nil {
				return warnings, err
			}
		}

		// Deletes: every current edge not in the desired set.
		for peerID := range current {
			if _, kept := desiredByID[peerID]; kept {
				continue
			}
			from, to := edgeEndpoints(entityID, peerID, incoming)
			err := em.DeleteRelationState(ctx, from, tailOf(current[peerID]), canonical, to)
			if errors.Is(err, store.ErrNotFound) {
				continue // a concurrent request deleted it first
			}
			if err != nil {
				return warnings, &relationError{
					RelType: canonical, Target: peerID, Op: "delete",
					Reason: "delete_failed", Err: err,
				}
			}
		}
	}
	return warnings, nil
}

// edgeWrite is one desired edge for upsertEdge.
type edgeWrite struct {
	from, to, relType     string
	ref                   v1.ResourceIdentifier
	exists                bool // the edge existed when the reconciler read it
	existingTail, newTail entity.Face
	props                 map[string]any
	content               string
}

// upsertEdge writes one desired edge: an update when it existed at read time,
// a create otherwise. That read is not held still, so a concurrent request may
// create or delete the same edge in between. A write that finds the edge in
// the other state switches to the other write, up to upsertEdgeAttempts
// times. A caller still losing after that gets the last error, which
// writeRelationsApplyError answers as a 409.
func (h *writeHandler) upsertEdge(ctx context.Context, w edgeWrite) error {
	exists, tail := w.exists, w.existingTail
	var err error
	for range upsertEdgeAttempts {
		if exists {
			err = h.writeUpdateRelation(ctx, w.from, tail, w.to, w.relType, w.ref)
			if !errors.Is(err, entitymanager.ErrRelationNotFound) {
				return err
			}
		} else {
			err = h.writeCreateRelation(ctx, w.from, w.newTail, w.to, w.relType, w.ref, w.props, w.content)
			if !errors.Is(err, entitymanager.ErrRelationAlreadyExists) {
				return err
			}
		}
		exists, tail = !exists, w.newTail
	}
	return err
}

// upsertEdgeAttempts bounds how often upsertEdge switches between update and
// create while concurrent requests keep flipping the edge.
const upsertEdgeAttempts = 8

// writeCreateRelation creates a new relation via the EntityManager
// (preserving the ACL, audit, validation and automation paths).
//
// Error handling splits three ways:
//
//   - ACL denial (*acl.ForbiddenError): propagated so the handler maps it
//     to 403. The EntityManager authorizes BEFORE any peer-existence check
//     (BUG-K6FEVB), so a denied write is a ForbiddenError even when the
//     peer is missing — it never reaches the fallback below.
//   - Missing peer (source/target entity not found): returned as a hard
//     structuralError so the handler maps it to 422. This is a DELIBERATE
//     reversal of DEC-HWZHA's soft-condition treatment for THIS case: the
//     old ungated fallback wrote the edge directly to the store, skipping
//     the ACL and audit (that is the --read-only bypass BUG-K6FEVB). The
//     user needs feedback that the reference did not resolve, so we reject
//     rather than silently warn — and we NEVER write directly to the store.
//   - Type-allowlist mismatch (invalid relation): still a soft condition —
//     both endpoints exist and a hand-editor could produce this state — so
//     it keeps DEC-HWZHA's write-with-warning behavior via a direct store
//     write. This write is safe because the ACL already allowed it above.
//
// `from` and `to` are pre-resolved by the caller via edgeEndpoints —
// this function does not consult direction. `ref.ID` is the peer ID
// (which is `to` for outgoing edges and `from` for incoming).
func (h *writeHandler) writeCreateRelation(
	ctx context.Context, from string, tail entity.Face, to, relType string, ref v1.ResourceIdentifier,
	finalProps map[string]any, finalContent string,
) error {
	opts := entity.RelationOptions{
		Properties: finalProps,
		Content:    ref.Content,
		FromFace:   tail,
	}
	_, err := h.manager.CreateRelation(ctx, from, relType, to, opts)
	if err == nil {
		return nil
	}
	if isMissingPeerCondition(err) {
		return danglingPeerError(relType, ref.ID)
	}
	if !isSoftCondition(err) {
		return &relationError{
			RelType: relType, Target: ref.ID, Op: "create",
			Reason: "create_failed", Err: err,
		}
	}
	// Soft condition (type-allowlist mismatch): write directly through
	// the store, skipping the workspace's pre-write validation. Safe
	// because the EntityManager already ran the ACL above.
	data := &store.RelationData{Properties: finalProps, Content: finalContent, FromFace: tail}
	if _, sErr := h.store.CreateRelation(ctx, from, relType, to, data); sErr != nil {
		return &relationError{
			RelType: relType, Target: ref.ID, Op: "create",
			Reason: "create_failed", Err: sErr,
		}
	}
	return nil
}

// writeUpdateRelation updates an existing relation, preferring the
// EntityManager path. Falls back to a direct store write on soft
// conditions, mirroring writeCreateRelation.
//
// `from` and `to` are pre-resolved by the caller via edgeEndpoints.
func (h *writeHandler) writeUpdateRelation(
	ctx context.Context, from string, tail entity.Face, to, relType string, ref v1.ResourceIdentifier,
) error {
	opts := entity.RelationOptions{
		Properties: ref.Meta,
		MetaUnset:  ref.MetaUnset,
		Content:    ref.Content,
		FromFace:   tail,
	}
	_, err := h.manager.UpdateRelation(ctx, from, relType, to, opts)
	if err == nil {
		return nil
	}
	if isMissingPeerCondition(err) {
		// See writeCreateRelation: a dangling peer is a hard 422, never a
		// silent ungated store write (BUG-K6FEVB).
		return danglingPeerError(relType, ref.ID)
	}
	if !isSoftCondition(err) {
		return &relationError{
			RelType: relType, Target: ref.ID, Op: "update",
			Reason: "update_failed", Err: err,
		}
	}
	// Soft condition: rebuild the post-merge state and write directly.
	//
	// FAILS CLOSED. mergeEdgeMeta reads a nil `current` as "no prior state",
	// so swallowing a store error here would ERASE the edge's existing
	// properties instead of merging into them — a silent partial write on a
	// transient fault.
	//
	// The read, merge and write share one Tx, like the manager's
	// UpdateRelation, so a concurrent update cannot land in between and be
	// overwritten by the merge of the row read before it.
	txErr := h.store.Tx(ctx, func(view store.Store) error {
		current, readErr := edgeOnFace(ctx, view, from, tail, relType, to)
		if readErr != nil && !errors.Is(readErr, store.ErrNotFound) {
			return readErr
		}
		finalProps, finalContent, _ := mergeEdgeMeta(current, ref)
		data := store.RelationData{Properties: finalProps, Content: finalContent}
		_, sErr := view.UpdateRelationState(ctx, from, tail, relType, to, data)
		return sErr
	})
	if txErr != nil {
		return &relationError{
			RelType: relType, Target: ref.ID, Op: "update",
			Reason: "update_failed", Err: txErr,
		}
	}
	return nil
}

// tailOfExistingEdge reports the TAIL the single-relation PATCH and DELETE
// routes should address on this triple.
//
// Two rules, in order, and the order is the point:
//
//  1. If the CALLER NAMED A FACE and the edge tails at the source they
//     addressed, that face IS the address. A triple can carry one edge per
//     tail, so discovering a tail instead would let `POL-1@draft` modify the
//     published edge — the very confusion an `@face` address exists to
//     resolve.
//  2. Otherwise the tail is read OFF THE EXISTING EDGE. A bare address names
//     no face, and on the INCOMING path the tail belongs to the peer and is
//     not this request's to choose; recomputing it from the request would
//     address a DIFFERENT edge and report success (RR-5MLZCR).
//
// Returns the zero face when neither rule finds one, which leaves the caller
// addressing the default tail and lets the manager's own not-found answer
// stand rather than inventing a distinct error here.
//
// addressed is the face the request named (zero when it named none); owned
// reports whether the addressed entity is this edge's SOURCE — false on the
// incoming path, where rule 1 must not apply.
func tailOfExistingEdge(
	ctx context.Context, st store.Store,
	from, relType, to string, addressed entity.Face, owned bool,
) entity.Face {
	if owned && !addressed.IsDefault() {
		return addressed
	}
	for rel, err := range st.ListRelations(ctx, store.RelationQuery{From: from, Type: relType, To: to}) {
		if err != nil {
			// A read fault is not evidence of a default-tail edge. The zero
			// face is the safe answer: the write then addresses the default
			// edge and fails not-found rather than hitting another face's.
			return entity.Face("")
		}
		if rel.From == from && rel.Type == relType && rel.To == to {
			return rel.FromFace
		}
	}
	return entity.Face("")
}

// edgeOnFace reads the edge of this triple whose TAIL is exactly tail.
//
// Package-level rather than a method because two unrelated surfaces need it —
// the relation write path's read-merge-write and relation-history restore —
// and the store has no face-aware relation get (`store.GetRelation` is the
// default tail by contract). A second copy of this query is how one of them
// ends up addressing the wrong edge.
//
// Nil: returns (nil, store.ErrNotFound) when no edge carries this tail; never
// (nil, nil). The callers act on the returned value, so "no such edge" must be
// distinguishable from "the read failed".
func edgeOnFace(
	ctx context.Context, st store.Store, from string, tail entity.Face, relType, to string,
) (*entity.Relation, error) {
	q := store.RelationQuery{From: from, FromFace: &tail, Type: relType, To: to}
	for rel, err := range st.ListRelations(ctx, q) {
		if err != nil {
			return nil, err
		}
		if rel.From == from && rel.FromFace == tail && rel.Type == relType && rel.To == to {
			return rel, nil
		}
	}
	return nil, store.ErrNotFound
}

// isMissingPeerCondition reports whether the EntityManager error is a
// dangling-peer error (source or target entity does not exist). This is
// treated as a HARD 422, not a soft warning (BUG-K6FEVB) — see
// danglingPeerError and writeCreateRelation for the rationale.
func isMissingPeerCondition(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "target entity not found") ||
		strings.Contains(msg, "source entity not found")
}

// isSoftCondition returns true when the error from EntityManager
// indicates a DEC-HWZHA "soft" condition that should be treated as a
// warning rather than blocking the write. The current workspace
// implementation surfaces these as plain fmt.Errorf strings; we match
// on substrings, which is fragile but acceptable for the current
// implementation surface.
//
// A missing peer is NOT a soft condition here (see isMissingPeerCondition);
// only the type-allowlist mismatch remains soft, because both endpoints
// exist and a hand-editor could produce that state.
func isSoftCondition(err error) bool {
	if err == nil {
		return false
	}
	// metamodel.ValidateRelation rejects type-allowlist failures.
	return strings.Contains(err.Error(), "invalid relation:")
}

// danglingPeerError builds the hard 422 returned when a relation write
// references a peer entity that does not exist. A structuralError maps to
// HTTP 422 via writeRelationsApplyError, telling the user the reference
// did not resolve (and so the edge was NOT stored). This deliberately
// reverses DEC-HWZHA's soft-warn treatment for the missing-peer case: the
// old behavior wrote the edge through an ungated direct store call that
// bypassed the ACL and audit (BUG-K6FEVB, the --read-only bypass).
func danglingPeerError(relType, peerID string) *structuralError {
	return &structuralError{
		Code:   "target_not_found",
		Path:   "/relations/" + v1.JSONPointerEscape(relType) + "/data",
		Detail: fmt.Sprintf("relation %q references entity %q, which does not exist", relType, peerID),
	}
}

// mergeEdgeMeta computes the post-merge (properties, content) tuple
// for an edge, given the existing relation (or nil for new edges) and
// the desired ref. contentSet reports whether ref.Content was non-nil
// (so the caller can distinguish "leave alone" from "set to value").
func mergeEdgeMeta(existing *entity.Relation, ref v1.ResourceIdentifier) (
	props map[string]any, content string, contentSet bool,
) {
	props = make(map[string]any)
	if existing != nil {
		maps.Copy(props, existing.Properties)
		content = existing.Content
	}
	maps.Copy(props, ref.Meta)
	for _, k := range ref.MetaUnset {
		delete(props, k)
	}
	if ref.Content != nil {
		content = *ref.Content
		contentSet = true
	}
	return props, content, contentSet
}

// isEdgeNoOp returns true when the post-merge state of an edge equals
// the existing edge byte-for-byte. Auto-save's primary path hits this:
// re-PATCHing a form that hasn't changed performs zero writes.
func isEdgeNoOp(
	existing *entity.Relation, finalProps map[string]any,
	finalContent string, contentSet bool, ref v1.ResourceIdentifier,
) bool {
	// If the request has no per-edge upsert fields at all and the
	// edge already exists, it's trivially a no-op. (Strict subset of
	// the value-based check below; kept as a fast path.)
	if ref.Meta == nil && ref.MetaUnset == nil && ref.Content == nil {
		return true
	}
	if !mapsEqual(existing.Properties, finalProps) {
		return false
	}
	if contentSet && existing.Content != finalContent {
		return false
	}
	return true
}

// mapsEqual is a small wrapper around reflect.DeepEqual that treats
// nil and empty maps as equal. (Go's reflect.DeepEqual considers
// nil != empty map, which would produce false-positive writes on
// edges whose existing properties are nil and whose final state is
// an empty map after merge.)
func mapsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// requiredMetaWarnings returns warnings for declared-required meta
// keys that are absent from the post-merge state.
//
// `direction` is the direction label ("outgoing" or "incoming") to
// stamp on the emitted Warning so UIs can disambiguate same-edge
// warnings without parsing paths. Per-edge meta is currently
// relation-type-scoped (not per-direction), so direction does not
// affect WHICH keys are required — it only labels the output.
func requiredMetaWarnings(
	relType string, relDef *metamodel.RelationDef, ref v1.ResourceIdentifier,
	finalProps map[string]any, dataPath, direction string,
) []Warning {
	var ws []Warning
	for k, propDef := range relDef.Properties {
		if !propDef.Required {
			continue
		}
		if _, ok := finalProps[k]; !ok {
			ws = append(ws, Warning{
				Code:      "required_meta_unset",
				Path:      fmt.Sprintf("%s[id=%s]/meta/%s", dataPath, v1.JSONPointerEscape(ref.ID), v1.JSONPointerEscape(k)),
				Detail:    fmt.Sprintf("relation type %q requires meta property %q", relType, k),
				Direction: direction,
			})
		}
	}
	return ws
}

// validateManagedOrderMeta enforces that values written into the managed
// order properties (_order_out / _order_in) are finite numeric. Non-finite
// or non-numeric values are wire-format violations (400) — the engine's
// midpoint and sort logic depends on finite float64 values.
func validateManagedOrderMeta(
	relType string, ref v1.ResourceIdentifier, edgePath string, relDef *metamodel.RelationDef,
) error {
	check := func(prop string) error {
		v, present := ref.Meta[prop]
		if !present {
			return nil
		}
		if _, ok := entitymanager.FiniteOrder(v); ok {
			return nil
		}
		return &v1.WireError{
			Code:   "order_value_invalid",
			Path:   edgePath + "/meta/" + v1.JSONPointerEscape(prop),
			Detail: fmt.Sprintf("relation type %q managed order property %q must be a finite number", relType, prop),
		}
	}
	if relDef.OutgoingOrderProperty() != "" {
		if err := check(metamodel.OrderPropertyOut); err != nil {
			return err
		}
	}
	if relDef.IncomingOrderProperty() != "" {
		if err := check(metamodel.OrderPropertyIn); err != nil {
			return err
		}
	}
	return nil
}

// structuralError is a typed hard-422 error from the modern reconciler:
// the request describes a state the storage layer can't represent. The
// HTTP handler maps it to 422 with the carried code.
type structuralError struct {
	Code   string
	Path   string
	Detail string
}

func (e *structuralError) Error() string {
	return fmt.Sprintf("%s: %s (path: %s)", e.Code, e.Detail, e.Path)
}

// asStructuralError extracts a *structuralError if err is or wraps one.
func asStructuralError(err error) (*structuralError, bool) {
	var se *structuralError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
