package visibility_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// failingReader fails every read, like a gated reader whose gate is down.
type failingReader struct{}

func (failingReader) GetEntity(context.Context, string) (*entity.Entity, error) {
	return nil, errors.New("gate down")
}

func (failingReader) ListEntities(context.Context, store.EntityQuery) iter.Seq2[*entity.Entity, error] {
	return func(yield func(*entity.Entity, error) bool) { yield(nil, errors.New("gate down")) }
}

func TestReadable(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	published := mustFace(t, "published")
	for _, e := range []*entity.Entity{
		ent("TKT-1", "ticket", nil),
		{ID: "POL-1", Type: "policy", Face: published},
	} {
		if err := st.CreateEntity(ctx, e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}

	tests := []struct {
		name string
		rd   visibility.RefReader
		ref  string
		want bool
	}{
		{"default row", st, "TKT-1", true},
		{"faced type by bare id", st, "POL-1", true},
		{"faced type by id@face", st, "POL-1@published", true},
		{"undeclared face of an existing id", st, "POL-1@draft", false},
		{"missing id", st, "TKT-404", false},
		{"read error counts as unreadable", failingReader{}, "TKT-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibility.Readable(ctx, tc.rd, tc.ref); got != tc.want {
				t.Errorf("Readable(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}
