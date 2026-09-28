package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// checkUniqueProperties enforces the `unique: true` natural-key
// constraint on e: for every property of e.Type declared unique, no other
// entity of the same type may carry the same non-empty value.
//
// It is called at every entity-write choke point right after
// [metamodel.Metamodel.ValidateEntity] — createCore (create), UpdateEntity
// (update), and ApplyEntity (sync). excludeSelfID names the entity's own
// ID so a re-save of an unchanged value does not collide with itself; pass
// "" on the create path.
//
// The check queries other entities and so cannot live in the pure,
// per-entity ValidateEntity. Violations are returned as
// [metamodel.ValidationError] values of type [metamodel.ValidationErrorUnique]
// (a HARD error) so the caller folds them into the same
// [newValidationError] → 422 path as structural validation failures.
//
// Semantics and limits:
//
//   - Empty values are exempt — a property is unique among the entities
//     that set it, matching the "email is unique for the people who have
//     one" intent. `list` properties are skipped (a natural key is a
//     scalar).
//   - Comparison is on the string value (identity keys — email, UPN — are
//     strings). Non-string property values are compared via their string
//     form and in practice only strings carry unique keys.
//   - The scan alone is a check-then-write. Callers make it atomic by running
//     the scan and the durable write through [writeWithUniqueCheck], which
//     puts both in one [store.Transactor.Tx] on the transaction's view. That
//     serializes against every other manager write that sets a unique
//     property on every backend: fs/mem/sqlite writes all take the Tx
//     mutex, and on PostgreSQL every such write goes through this same Tx.
//     pgstore additionally backstops the write with a derived partial unique
//     index (TKT-3Q0GP1) whose violation surfaces as
//     [store.UniquePropertyError], mapped to the SAME 422 this scan
//     produces. See docs/acl-security.md and docs/postgres-backend.md.
//
// st is the store to scan: the Tx view when called from inside a
// transaction, never the outer store (an outer-store call inside a Tx
// callback bypasses the transaction on pg and can deadlock on fs).
func checkUniqueProperties(
	ctx context.Context, meta *metamodel.Metamodel, st store.Store, e *entity.Entity, excludeSelfID string,
) error {
	// Skip the scan entirely when e sets no unique value — the common case
	// for types without a natural key pays nothing.
	toCheck := uniqueValues(meta, e)
	if len(toCheck) == 0 {
		return nil
	}

	// AllStates, then filtered to THIS face. Without it the scan sees only
	// zero-coordinate rows, which a type declaring faces has none of — so
	// `unique:` silently enforced nothing on exactly the types most likely
	// to want it (BUG-HC6I2T).
	//
	// PER-FACE is the rule: two ENTITIES may not share the value within one
	// face, and two faces of ONE entity never collide, because they are the
	// same entity. Excluding by bare id rather than by row is what makes the
	// second half true. Whether an operator can ask for the stronger
	// per-entity reading is TKT-HXT2P9.
	var violations []*metamodel.ValidationError
	q := store.EntityQuery{Type: e.Type, AllStates: true}
	for other, err := range st.ListEntities(ctx, q) {
		if err != nil {
			// A partial scan cannot prove uniqueness — fail the write loud
			// rather than admit a possible duplicate.
			return fmt.Errorf("entitymanager: unique check for %s: %w", e.ID, err)
		}
		if other.Face != e.Face {
			continue
		}
		if other.ID == excludeSelfID || other.ID == e.ID {
			continue
		}
		for _, up := range toCheck {
			if other.GetString(up.name) == up.value {
				// Client-facing message names only the property + type — NOT
				// the colliding entity's ID or the value. For an identity key
				// (e.g. persoon.email) leaking "PERS-X already has alice@corp"
				// to whoever attempted the write is an enumeration oracle
				// (confirms an entity/value is registered). The full detail is
				// logged server-side instead, mirroring the RR-372L discipline.
				slog.Warn("entitymanager: unique constraint violated",
					"type", e.Type, "property", up.name,
					"attempted_by", e.ID, "conflicts_with", other.ID)
				violations = append(violations, &metamodel.ValidationError{
					Type:     metamodel.ValidationErrorUnique,
					Property: up.name,
					Message: fmt.Sprintf(
						"property %q must be unique for type %q; another entity already has this value",
						up.name, e.Type),
				})
			}
		}
	}
	if len(violations) > 0 {
		return newValidationError(violations)
	}
	return nil
}

// uniqueProp is one unique, non-list property e sets to a non-empty value.
type uniqueProp struct {
	name  string
	value string
}

// uniqueValues returns the unique, non-list properties e sets to a non-empty
// value; empty when e sets none.
func uniqueValues(meta *metamodel.Metamodel, e *entity.Entity) []uniqueProp {
	def, ok := meta.GetEntityDef(e.Type)
	if !ok { // coverage-ignore: defensive: every caller runs ValidateEntity first, which hard-rejects an unknown type
		// (ValidationErrorUnknownType is not soft), so an unknown type here needs a metamodel reload race
		return nil // unknown type is caught by ValidateEntity's own path
	}
	var out []uniqueProp
	for name, pd := range def.PropertyDefs() {
		if !pd.Unique || pd.List {
			continue
		}
		if v := e.GetString(name); v != "" {
			out = append(out, uniqueProp{name: name, value: v})
		}
	}
	return out
}

// writeWithUniqueCheck runs write with e's `unique:` constraint enforced
// atomically against it (see [checkUniqueProperties] for the semantics).
//
// When e sets no unique value, write runs directly against deps.Store and no
// transaction is opened. Otherwise the scan and write run inside one
// [store.Transactor.Tx], both against the transaction's view: write receives
// that view and must issue every store call through it.
//
// The callback holds a schema-wide lock on pgstore and the write mutex on
// fs/mem/sqlite, so write must be a plain store write — never a public
// Manager call (automations, audit, cascades and version capture stay
// outside), and never slow external I/O.
func writeWithUniqueCheck(
	ctx context.Context, deps Deps, e *entity.Entity, excludeSelfID string, write func(st store.Store) error,
) error {
	if len(uniqueValues(deps.Meta, e)) == 0 {
		return write(deps.Store)
	}
	return deps.Store.Tx(ctx, func(view store.Store) error {
		if err := checkUniqueProperties(ctx, deps.Meta, view, e, excludeSelfID); err != nil {
			return err
		}
		return write(view)
	})
}

// mapUniquePropertyConflict translates a store-level derived-unique-index
// violation ([store.UniquePropertyError], raised atomically by a pgstore partial
// unique index — TKT-3Q0GP1) into the SAME [ValidationError] the pre-write
// [checkUniqueProperties] scan produces, so a client cannot tell which
// enforcement path caught the duplicate: both yield a 422 naming the property
// and withholding the colliding value. It returns the original error unchanged
// when it is not an UniquePropertyError.
//
// This is the second-line backstop to the scan: the scan wins the common case
// (and is the only mechanism on fs/mem), but a concurrent writer that passed
// the scan is stopped atomically by the index and lands here. When the store
// could not attribute the violation to a property (empty Property — e.g. a
// rolling deploy against a peer-created index), it degrades to a generic
// property-less unique error rather than inventing a property name.
//
// ok reports whether err was a UniquePropertyError (and thus mapped); when
// false the returned error is err unchanged, so callers write
// `if ok, v := mapUniquePropertyConflict(err); ok { return v }`.
func mapUniquePropertyConflict(err error) (ok bool, mapped error) {
	var up store.UniquePropertyError
	if !errors.As(err, &up) {
		return false, err
	}
	msg := "a property that must be unique already has this value"
	if up.Property != "" {
		msg = fmt.Sprintf(
			"property %q must be unique; another entity already has this value", up.Property)
	}
	return true, newValidationError([]*metamodel.ValidationError{{
		Type:     metamodel.ValidationErrorUnique,
		Property: up.Property,
		Message:  msg,
	}})
}
