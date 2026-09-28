package visibility

import (
	"context"
	"iter"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// UnrestrictedReader is a script read handle that applies NO gate and NO
// redaction: every read goes straight to the store. Build one with
// [Unrestricted].
//
// Why this type exists at all, given it does nothing: the Lua read surface
// (internal/lua's EntityReader) is satisfied STRUCTURALLY by store.Store,
// so a gated wiring and an ungated one are indistinguishable when reading
// a struct literal — `VisibleReader: st` and `VisibleReader: gatedReader`
// look equally deliberate. Three production sites were silently ungated
// through exactly that blind spot (RR-R0G3DF). Naming the ungated choice
// makes it greppable:
//
//	grep -rn "visibility.Unrestricted" --include=*.go
//
// enumerates every ungated script read path in the tree, in one command.
//
// This is LEGIBILITY, not enforcement. The type system still accepts a
// bare store.Store wherever this is accepted; nothing stops a future
// wiring from skipping it. What it buys is that an ungated path can no
// longer be created by accident or survive review unnoticed — it has to be
// spelled out.
//
// Deliberately NOT a store.Store: it exposes the three read methods and
// nothing else, so it cannot be passed where a full store is wanted (a
// write path, say) and cannot silently widen back into one.
type UnrestrictedReader struct {
	st store.Store
}

// Unrestricted wraps a raw store as an explicitly ungated script read
// handle.
//
// It PANICS on a nil store, per CLAUDE.md "constructors reject nil
// required fields". Returning a nil *UnrestrictedReader instead would be
// actively dangerous: assigning a typed nil pointer into the
// lua.EntityReader interface field produces a NON-nil interface, so lua's
// `VisibleReader == nil` deny guard (runtime.go, RR-X9NVHI) is skipped and
// the first read nil-derefs inside GetEntity. Nothing on the script paths
// recovers, so that panic takes the process down at request time rather
// than raising the clean "no reader is configured" Lua error the deny path
// produces. A nil store here is a wiring bug: failing loudly at
// construction, in the stack of the code that made the mistake, is
// strictly better than either alternative.
//
// Legitimate uses are paths where an ACL cannot apply or would be wrong:
//
//   - The operator trust boundary — CLI and docs runtimes, where whoever
//     runs the binary already has the project files, so gating the reads
//     would protect nothing (RR-17DMC).
//   - Validation rule bodies, where redacting incidental cross-entity
//     lookups manufactures false violations rather than hiding anything:
//     a rule asserting "every ticket links to a project" would fire on
//     projects the acting principal cannot see.
//   - A throwaway store the caller just seeded itself.
//
// If a new call site does not clearly fall into one of those, it probably
// wants the ACL-bound reader instead — see [NewScriptReader].
func Unrestricted(st store.Store) *UnrestrictedReader {
	if st == nil {
		panic("visibility.Unrestricted: store must be non-nil")
	}
	return &UnrestrictedReader{st: st}
}

// GetEntity implements the script read surface: a pass-through that accepts
// an address (`ID` or `ID@face`), as the gated [ScriptReader] does.
func (r *UnrestrictedReader) GetEntity(ctx context.Context, addr string) (*entity.Entity, error) {
	return store.GetEntityAt(ctx, r.st, addr)
}

// ListEntities implements the script read surface: straight pass-through.
func (r *UnrestrictedReader) ListEntities(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[*entity.Entity, error] {
	return r.st.ListEntities(ctx, q)
}

// ListEntityHeaders implements the script read surface: straight
// pass-through to the store's header projection (or its fallback).
func (r *UnrestrictedReader) ListEntityHeaders(
	ctx context.Context, q store.EntityQuery,
) iter.Seq2[store.EntityHeader, error] {
	return store.ListEntityHeaders(ctx, r.st, q)
}

// ListRelations implements the script read surface: straight pass-through.
func (r *UnrestrictedReader) ListRelations(
	ctx context.Context, q store.RelationQuery,
) iter.Seq2[*entity.Relation, error] {
	return r.st.ListRelations(ctx, q)
}
