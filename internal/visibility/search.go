package visibility

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/search"
)

// MaxSearchLimit caps the hits one gated search may return. The gated path
// loads and evaluates every candidate hit, so a caller-chosen unlimited query
// would turn one request into thousands of verdict evaluations. The value
// matches the data-entry free-text cap.
const MaxSearchLimit = 1000

// SearchScoper is the policy half of a [Searcher]: it binds one per-operation
// ACL scope and answers the per-type list-read verdict and face set.
// [DeclarativeGate] is the production implementation.
type SearchScoper interface {
	RowGate
	FaceGate
	Bind(ctx context.Context) (context.Context, error)
	ReadQueryFor(ctx context.Context, entityType string) (acl.ReadQueryResult, error)
}

// Searcher is a [search.Searcher] whose hits are gated for the principal on
// ctx at call time. It applies the same rules as the data-entry search path:
//
//   - Row scope: each type's list-read verdict becomes a [search.TypeScope].
//     A type without a grant is absent from the scope, which denies it. No
//     wildcard is emitted, so an entity whose type is not in the metamodel is
//     not searchable under a policy.
//   - Field scope: a hit whose text matched only `visible:`-hidden properties
//     is dropped (the match-on-hidden-field oracle, TKT-GGQ0JT).
//   - Face scope: a hit on a face the principal may not read is dropped.
//
// [search.Hit.Title] is cleared on every hit. It is the raw indexed value, and
// a hit that matched on the id or body survives even when the title property
// is hidden. Consumers take the title from the gated reader.
//
// A query with Filters or Sort is refused. The hidden-field check covers the
// free text only, so a filter or sort on a hidden property would reveal its
// value. No gated consumer needs either today.
//
// Every failure is fail-closed: an error is yielded and no further hits are.
type Searcher struct {
	vs     search.FieldVisibleSearcher
	scoper SearchScoper
	redact FieldRedactor
	types  []string
}

var _ search.Searcher = (*Searcher)(nil)

// NewSearcher builds a gated searcher. types is the set of metamodel entity
// types a query may reach; a query naming other types matches nothing.
//
// Nil: vs, scoper and redact are rejected — a missing collaborator would
// otherwise surface as ungated or empty results.
//
// vs must implement [search.FieldVisibleSearcher]; a searcher that cannot
// filter hidden-field matches is rejected here rather than degrading later.
func NewSearcher(
	vs search.VisibleSearcher, scoper SearchScoper, redact FieldRedactor, types []string,
) (*Searcher, error) {
	if vs == nil {
		return nil, errors.New("visibility: NewSearcher: VisibleSearcher is required")
	}
	if scoper == nil {
		return nil, errors.New("visibility: NewSearcher: SearchScoper is required")
	}
	if redact == nil {
		return nil, errors.New("visibility: NewSearcher: FieldRedactor is required")
	}
	fvs, ok := vs.(search.FieldVisibleSearcher)
	if !ok {
		return nil, fmt.Errorf("visibility: NewSearcher: %T cannot filter hidden-field matches", vs)
	}
	return &Searcher{vs: fvs, scoper: scoper, redact: redact, types: slices.Clone(types)}, nil
}

// Search implements [search.Searcher].
func (s *Searcher) Search(ctx context.Context, q search.Query) iter.Seq2[search.Hit, error] {
	return func(yield func(search.Hit, error) bool) {
		if len(q.Filters) > 0 || len(q.Sort) > 0 {
			yield(search.Hit{}, fmt.Errorf("%w: property filters and sorting are not supported", search.ErrScope))
			return
		}
		bound, err := s.scoper.Bind(ctx)
		if err != nil {
			yield(search.Hit{}, fmt.Errorf("%w: %w", search.ErrScope, err))
			return
		}
		scope, err := s.scope(bound, q.Types)
		if err != nil {
			yield(search.Hit{}, fmt.Errorf("%w: %w", search.ErrScope, err))
			return
		}
		if len(scope) == 0 {
			return
		}
		if q.Limit <= 0 || q.Limit > MaxSearchLimit {
			q.Limit = MaxSearchLimit
		}
		for h, err := range s.vs.SearchVisibleFields(bound, q, scope, s.hiddenFields) {
			if err != nil {
				yield(search.Hit{}, err)
				return
			}
			if !FaceAllowed(bound, s.scoper, h.Type, h.Face) {
				continue
			}
			h.Title = ""
			if !yield(h, nil) {
				return
			}
		}
	}
}

// scope maps each reachable type's list-read verdict onto a search scope.
// Only the types the query names are consulted, when it names any.
func (s *Searcher) scope(ctx context.Context, want []string) (map[string]search.TypeScope, error) {
	scope := make(map[string]search.TypeScope, len(s.types))
	for _, typ := range s.types {
		if len(want) > 0 && !slices.Contains(want, typ) {
			continue
		}
		rqr, err := s.scoper.ReadQueryFor(ctx, typ)
		if err != nil {
			return nil, err
		}
		switch {
		case rqr.AllowAll:
			scope[typ] = search.TypeScope{AllowAll: true}
		case rqr.Query != nil:
			scope[typ] = search.TypeScope{Query: rqr.Query}
		}
	}
	return scope, nil
}

// hiddenFields is the [search.HiddenFieldsFunc]: the redactor's hidden
// property names, qualified into the search field vocabulary. A bare name
// would never match a `prop:`-qualified field, which would silently keep
// every hidden-field match.
func (s *Searcher) hiddenFields(
	ctx context.Context, _ search.Hit, e *entity.Entity,
) (map[string]struct{}, error) {
	hidden := s.redact.HiddenProperties(ctx, e)
	out := make(map[string]struct{}, len(hidden))
	for name := range hidden {
		out[search.PropFieldPrefix+name] = struct{}{}
	}
	return out, nil
}

// DenySearcher refuses every search with [ErrReaderUnavailable]. It is the
// search counterpart of [DenyReader]: the substitute when a policy is
// configured but the gated [Searcher] could not be built.
type DenySearcher struct{}

var _ search.Searcher = DenySearcher{}

// Search implements [search.Searcher]: always refuses.
func (DenySearcher) Search(context.Context, search.Query) iter.Seq2[search.Hit, error] {
	return func(yield func(search.Hit, error) bool) {
		yield(search.Hit{}, ErrReaderUnavailable)
	}
}
