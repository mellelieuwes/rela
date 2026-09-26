package visibility

import (
	"context"
	"iter"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// RefReader is the read surface [Readable] needs. A gated reader answers for
// the ctx principal; a raw store answers for everyone.
type RefReader interface {
	GetEntity(ctx context.Context, id string) (*entity.Entity, error)
	ListEntities(ctx context.Context, q store.EntityQuery) iter.Seq2[*entity.Entity, error]
}

// Readable reports whether r returns the entity ref names. ref is a bare id or
// the boundary form `ID@face`, resolved the way the write path resolves a
// relation endpoint: the default row, else the named face, else any face of a
// faced type (which has no default row).
//
// Pass a gated reader to ask "may the caller read this", before a write that
// names the id. The write path answers differently for a hidden entity and a
// missing one, so asking it first would confirm the entity exists. A read
// error counts as not readable.
func Readable(ctx context.Context, r RefReader, ref string) bool {
	if e, err := r.GetEntity(ctx, ref); err == nil && e != nil {
		return true
	}
	q := store.EntityQuery{IDs: []string{ref}, AllStates: true}
	if base, face, err := entity.ParseStateRef(ref); err == nil && !face.IsDefault() {
		q = store.EntityQuery{IDs: []string{base}, FaceIn: []entity.Face{face}}
	}
	for e, err := range r.ListEntities(ctx, q) {
		if err == nil && e != nil && e.ID == q.IDs[0] {
			return true
		}
	}
	return false
}
