package visibility_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/search"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/visibility"
)

// fakeScoper grants per type from a fixed table. A type absent from reads is
// denied, like a policy with no grant for it.
type fakeScoper struct {
	reads   map[string]acl.ReadQueryResult
	bindErr error
	readErr error
}

func (f fakeScoper) Bind(ctx context.Context) (context.Context, error) {
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	return ctx, nil
}

func (f fakeScoper) ReadQueryFor(_ context.Context, typ string) (acl.ReadQueryResult, error) {
	if f.readErr != nil {
		return acl.ReadQueryResult{}, f.readErr
	}
	if r, ok := f.reads[typ]; ok {
		return r, nil
	}
	return acl.ReadQueryResult{DenyAll: true}, nil
}

func (f fakeScoper) PermittedFaces(ctx context.Context, typ string) ([]entity.Face, error) {
	r, err := f.ReadQueryFor(ctx, typ)
	return r.Faces, err
}

func (f fakeScoper) PermitsRead(context.Context, string, string) (bool, error) { return true, nil }

func (f fakeScoper) PermitsReadMany(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

// hideProps hides the named properties on every entity.
type hideProps []string

func (h hideProps) HiddenProperties(context.Context, *entity.Entity) map[string]struct{} {
	out := make(map[string]struct{}, len(h))
	for _, n := range h {
		out[n] = struct{}{}
	}
	return out
}

var allowAll = acl.ReadQueryResult{AllowAll: true}

// newLinearVisible seeds a memstore with a linear index and returns the
// generic FieldVisibleSearcher over it.
func newLinearVisible(t *testing.T, ents ...*entity.Entity) search.VisibleSearcher {
	t.Helper()
	ls := search.NewLinearSearch()
	st := memstore.New(memstore.WithObserver(ls))
	for _, e := range ents {
		if err := st.CreateEntity(context.Background(), e); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
	v, err := search.NewVisible(search.New(st, ls), st)
	if err != nil {
		t.Fatalf("NewVisible: %v", err)
	}
	return v
}

func ent(id, typ string, props map[string]any) *entity.Entity {
	return &entity.Entity{ID: id, Type: typ, Properties: props}
}

func collect(t *testing.T, seq iter.Seq2[search.Hit, error]) ([]search.Hit, error) {
	t.Helper()
	var hits []search.Hit
	for h, err := range seq {
		if err != nil {
			return hits, err
		}
		hits = append(hits, h)
	}
	return hits, nil
}

func ids(hits []search.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.ID)
	}
	slices.Sort(out)
	return out
}

func TestSearcher_Gating(t *testing.T) {
	vs := newLinearVisible(t,
		ent("TKT-1", "ticket", map[string]any{"title": "alpha ticket"}),
		ent("FEAT-1", "feature", map[string]any{"title": "alpha feature"}),
		ent("PERS-1", "person", map[string]any{"title": "Alice", "salary": "zebra99"}),
		ent("GHOST-1", "ghost", map[string]any{"title": "alpha ghost"}),
	)
	types := []string{"ticket", "feature", "person"}

	tests := []struct {
		name   string
		reads  map[string]acl.ReadQueryResult
		hidden hideProps
		query  search.Query
		want   []string
	}{
		{
			name:  "type without a grant is not searchable",
			reads: map[string]acl.ReadQueryResult{"ticket": allowAll},
			query: search.Query{Text: "alpha"},
			want:  []string{"TKT-1"},
		},
		{
			name:  "type outside the metamodel is not searchable",
			reads: map[string]acl.ReadQueryResult{"ticket": allowAll, "feature": allowAll, "ghost": allowAll},
			query: search.Query{Text: "alpha"},
			want:  []string{"FEAT-1", "TKT-1"},
		},
		{
			name:   "match on a hidden property only is dropped",
			reads:  map[string]acl.ReadQueryResult{"person": allowAll},
			hidden: hideProps{"salary"},
			query:  search.Query{Text: "zebra99"},
			want:   []string{},
		},
		{
			name:  "match on a property that is not hidden is kept",
			reads: map[string]acl.ReadQueryResult{"person": allowAll},
			query: search.Query{Text: "zebra99"},
			want:  []string{"PERS-1"},
		},
		{
			name:  "query types narrow the scope",
			reads: map[string]acl.ReadQueryResult{"ticket": allowAll, "feature": allowAll},
			query: search.Query{Text: "alpha", Types: []string{"feature"}},
			want:  []string{"FEAT-1"},
		},
		{
			name:  "no grant at all yields nothing",
			reads: nil,
			query: search.Query{Text: "alpha"},
			want:  []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := visibility.NewSearcher(vs, fakeScoper{reads: tc.reads}, tc.hidden, types)
			if err != nil {
				t.Fatalf("NewSearcher: %v", err)
			}
			hits, err := collect(t, s.Search(context.Background(), tc.query))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if got := ids(hits); !slices.Equal(got, tc.want) {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
			for _, h := range hits {
				if h.Title != "" {
					t.Errorf("hit %s carries index title %q; titles must come from the gated reader", h.ID, h.Title)
				}
			}
		})
	}
}

// stubSearcher returns canned hits and records the query it was given.
type stubSearcher struct {
	hits []search.Hit
	err  error
	got  *search.Query
}

func (s stubSearcher) SearchVisible(
	ctx context.Context, q search.Query, scope map[string]search.TypeScope,
) iter.Seq2[search.Hit, error] {
	return s.SearchVisibleFields(ctx, q, scope, nil)
}

func (s stubSearcher) SearchVisibleFields(
	_ context.Context, q search.Query, _ map[string]search.TypeScope, _ search.HiddenFieldsFunc,
) iter.Seq2[search.Hit, error] {
	if s.got != nil {
		*s.got = q
	}
	return func(yield func(search.Hit, error) bool) {
		if s.err != nil {
			yield(search.Hit{}, s.err)
			return
		}
		for _, h := range s.hits {
			if !yield(h, nil) {
				return
			}
		}
	}
}

func mustFace(t *testing.T, s string) entity.Face {
	t.Helper()
	f, err := entity.ParseFace(s)
	if err != nil {
		t.Fatalf("ParseFace(%q): %v", s, err)
	}
	return f
}

func TestSearcher_FaceGate(t *testing.T) {
	stub := stubSearcher{hits: []search.Hit{
		{ID: "POL-1", Type: "policy", Face: mustFace(t, "published")},
		{ID: "POL-2", Type: "policy", Face: mustFace(t, "draft")},
	}}
	scoper := fakeScoper{reads: map[string]acl.ReadQueryResult{
		"policy": {AllowAll: true, Faces: []entity.Face{mustFace(t, "published")}},
	}}
	s, err := visibility.NewSearcher(stub, scoper, hideProps(nil), []string{"policy"})
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}
	hits, err := collect(t, s.Search(context.Background(), search.Query{Text: "x"}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := ids(hits); !slices.Equal(got, []string{"POL-1"}) {
		t.Errorf("hits = %v, want [POL-1]: a draft-face hit reached a published-only reader", got)
	}
}

func TestSearcher_ClampsLimit(t *testing.T) {
	for _, limit := range []int{0, -1, visibility.MaxSearchLimit + 1} {
		var got search.Query
		stub := stubSearcher{got: &got}
		scoper := fakeScoper{reads: map[string]acl.ReadQueryResult{"ticket": allowAll}}
		s, err := visibility.NewSearcher(stub, scoper, hideProps(nil), []string{"ticket"})
		if err != nil {
			t.Fatalf("NewSearcher: %v", err)
		}
		if _, err := collect(t, s.Search(context.Background(), search.Query{Text: "x", Limit: limit})); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got.Limit != visibility.MaxSearchLimit {
			t.Errorf("limit %d reached the index as %d, want %d", limit, got.Limit, visibility.MaxSearchLimit)
		}
	}
}

func TestSearcher_FailsClosed(t *testing.T) {
	boom := errors.New("boom")
	hit := []search.Hit{{ID: "TKT-1", Type: "ticket"}}
	tests := []struct {
		name   string
		scoper fakeScoper
		stub   stubSearcher
	}{
		{"bind failure", fakeScoper{bindErr: boom}, stubSearcher{hits: hit}},
		{"scope failure", fakeScoper{readErr: boom}, stubSearcher{hits: hit}},
		{"search failure", fakeScoper{reads: map[string]acl.ReadQueryResult{"ticket": allowAll}}, stubSearcher{err: boom}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := visibility.NewSearcher(tc.stub, tc.scoper, hideProps(nil), []string{"ticket"})
			if err != nil {
				t.Fatalf("NewSearcher: %v", err)
			}
			hits, err := collect(t, s.Search(context.Background(), search.Query{Text: "x"}))
			if !errors.Is(err, boom) {
				t.Errorf("err = %v, want boom", err)
			}
			if len(hits) != 0 {
				t.Errorf("hits = %v, want none on failure", ids(hits))
			}
		})
	}
}

// entityOnlySearcher is a VisibleSearcher that cannot filter hidden-field
// matches.
type entityOnlySearcher struct{}

func (entityOnlySearcher) SearchVisible(
	context.Context, search.Query, map[string]search.TypeScope,
) iter.Seq2[search.Hit, error] {
	return func(func(search.Hit, error) bool) {}
}

func TestNewSearcher_Rejects(t *testing.T) {
	vs := stubSearcher{}
	scoper := fakeScoper{}
	tests := []struct {
		name   string
		vs     search.VisibleSearcher
		scoper visibility.SearchScoper
		redact visibility.FieldRedactor
	}{
		{"nil searcher", nil, scoper, hideProps(nil)},
		{"nil scoper", vs, nil, hideProps(nil)},
		{"nil redactor", vs, scoper, nil},
		{"searcher without field filtering", entityOnlySearcher{}, scoper, hideProps(nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := visibility.NewSearcher(tc.vs, tc.scoper, tc.redact, nil); err == nil {
				t.Error("NewSearcher accepted it")
			}
		})
	}
}

// TestSearcher_RefusesFiltersAndSort (RR-QLJWO7): filters and sorts are not
// covered by the hidden-field check, so a query using them is refused.
func TestSearcher_RefusesFiltersAndSort(t *testing.T) {
	scoper := fakeScoper{reads: map[string]acl.ReadQueryResult{"person": allowAll}}
	stub := stubSearcher{hits: []search.Hit{{ID: "PERS-1", Type: "person"}}}
	s, err := visibility.NewSearcher(stub, scoper, hideProps{"salary"}, []string{"person"})
	if err != nil {
		t.Fatalf("NewSearcher: %v", err)
	}
	for name, q := range map[string]search.Query{
		"filter": {Filters: []search.PropertyFilter{{Property: "salary", Value: "99000"}}},
		"sort":   {Sort: []search.SortClause{{Field: "salary"}}},
	} {
		hits, err := collect(t, s.Search(context.Background(), q))
		if !errors.Is(err, search.ErrScope) || len(hits) != 0 {
			t.Errorf("%s: hits=%v err=%v, want a refusal", name, ids(hits), err)
		}
	}
}
