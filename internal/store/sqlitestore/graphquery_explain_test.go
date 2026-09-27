package sqlitestore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/predicate"
	"github.com/Sourcehaven-BV/rela/internal/queryplan"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/sqlitestore"
)

// The EXPLAIN QUERY PLAN tests pin the plan SHAPE of the SQL path, as
// pgstore's EXPLAIN tests do: that a derived index is used, and that a page
// walks an index instead of sorting. They assert names in the plan, not
// timings, so they are stable in CI. No ANALYZE runs: the derived indexes must
// be chosen from the query shape alone, which is what a fresh database gets.

// seed creates entities and relations in one transaction.
func seed(t *testing.T, s *sqlitestore.Store, fn func(store.Store)) {
	t.Helper()
	require.NoError(t, s.Tx(context.Background(), func(v store.Store) error {
		fn(v)
		return nil
	}))
}

func mustCreate(t *testing.T, s store.Store, e *entity.Entity) {
	t.Helper()
	require.NoError(t, s.CreateEntity(context.Background(), e))
}

func mustRelate(t *testing.T, s store.Store, from, typ, to string) {
	t.Helper()
	_, err := s.CreateRelation(context.Background(), from, typ, to, nil)
	require.NoError(t, err)
}

func explain(t *testing.T, s *sqlitestore.Store, q store.GraphQuery) string {
	t.Helper()
	plan, err := s.ExplainGraphQuery(context.Background(), q)
	require.NoError(t, err)
	t.Logf("plan:\n%s", plan)
	return plan
}

func reconcile(t *testing.T, s *sqlitestore.Store, specs []store.DerivedObjectSpec) []store.DerivedObjectOutcome {
	t.Helper()
	out, err := sqlitestore.Reconcile(context.Background(), s, specs, store.ReconcileOptions{})
	require.NoError(t, err)
	for _, o := range out {
		require.NotEqual(t, store.DerivedUnenforced, o.State, o.Reason)
	}
	return out
}

// The endpoint closure walks relations by from_id through the primary key,
// once per step, rather than scanning the table each iteration.
func TestGraphQueryExplainClosureUsesRelationIndex(t *testing.T) {
	s := open(t)
	seed(t, s, func(v store.Store) {
		mustCreate(t, v, entity.New("alice", "person"))
		mustCreate(t, v, entity.New("engineering", "team"))
		mustRelate(t, v, "alice", "member-of", "engineering")
		for i := range 2000 {
			id := fmt.Sprintf("TKT-%06d", i)
			mustCreate(t, v, entity.New(id, "ticket"))
			if i%10 == 0 {
				mustRelate(t, v, "engineering", "owns", id)
			}
		}
	})
	plan := explain(t, s, store.GraphQuery{
		EntityType: "ticket",
		HasInbound: &store.RelationPredicate{
			Endpoints: []string{"alice"}, OfTypes: []string{"owns"},
			InheritThrough: []string{"member-of"}, Depth: 5,
		},
	})
	require.Contains(t, plan, "SEARCH r USING COVERING INDEX sqlite_autoindex_relations_1 (from_id=?)",
		"the closure step does not walk relations by from_id")
	requireNoTableScan(t, plan)
}

func TestGraphQueryExplainUsesDerivedStaticQueryIndex(t *testing.T) {
	s := open(t)
	reconcile(t, s, []store.DerivedObjectSpec{{
		Kind: store.DerivedQueryIndex, Type: "task", Properties: []string{"status"},
	}})
	seed(t, s, func(v store.Store) {
		for i := range 2000 {
			e := entity.New(fmt.Sprintf("TASK-%06d", i), "task")
			e.Properties["status"] = "closed"
			mustCreate(t, v, e)
		}
	})
	plan := explain(t, s, store.GraphQuery{
		EntityType: "task",
		Props:      []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true}},
	})
	require.Contains(t, plan, "rela_derived_query__")
}

// A pushed list page (filter on one property, order by another, LIMIT) walks
// the derived list index instead of sorting the type.
func TestGraphQueryExplainPagedListUsesDerivedListIndex(t *testing.T) {
	s := open(t)
	reconcile(t, s, []store.DerivedObjectSpec{{
		Kind: store.DerivedListIndex, Type: "task", Properties: []string{"status"}, OrderBy: []string{"due"},
	}})
	seed(t, s, func(v store.Store) {
		for i := range 2000 {
			e := entity.New(fmt.Sprintf("TASK-%06d", i), "task")
			e.Properties["status"] = []string{"open", "done"}[i%2]
			e.Properties["due"] = fmt.Sprintf("2026-%02d-%02d", 1+i%12, 1+i%28)
			mustCreate(t, v, e)
		}
	})
	plan := explain(t, s, store.GraphQuery{
		EntityType: "task",
		Props:      []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true}},
		OrderBy:    []store.OrderSpec{{Property: "due"}},
		Limit:      25,
	})
	require.Contains(t, plan, "rela_derived_list__")
	require.NotContains(t, plan, "TEMP B-TREE", "the page sorts instead of walking the index")
}

// An enum-ranked sort reaches the list index too: the rank CASE is spelled the
// same way in the query and in the DDL.
func TestGraphQueryExplainRankedListUsesDerivedListIndex(t *testing.T) {
	s := open(t)
	values := []string{"backlog", "ready", "done"}
	reconcile(t, s, []store.DerivedObjectSpec{{
		Kind: store.DerivedListIndex, Type: "task", OrderBy: []string{"stage"},
		OrderValues: [][]string{values},
	}})
	seed(t, s, func(v store.Store) {
		for i := range 2000 {
			e := entity.New(fmt.Sprintf("TASK-%06d", i), "task")
			e.Properties["stage"] = values[i%3]
			mustCreate(t, v, e)
		}
	})
	plan := explain(t, s, store.GraphQuery{
		EntityType: "task",
		OrderBy:    []store.OrderSpec{{Property: "stage", Values: values}},
		Limit:      25,
	})
	require.Contains(t, plan, "rela_derived_list__")
	require.NotContains(t, plan, "TEMP B-TREE")
}

// An endpoint filter never scans the traversed-to type. Without ANALYZE,
// SQLite drives from the candidate side and checks each edge's endpoint by
// primary key; the derived index queryplan asks for is created (the reconcile
// helper fails on an unenforced spec) and is what the planner picks once the
// statistics make the endpoint side the cheaper driver. Either plan is
// index-only, which is the property pinned here.
func TestEndpointMatchExplainIsIndexOnly(t *testing.T) {
	s := open(t)
	meta, err := metamodel.Parse([]byte(`version: "1.0"
namespace: https://example.org/test#
types:
  concept_status: {values: [active, rare]}
entities:
  ticket: {label: Ticket, id_prefix: TKT, properties: {title: {type: string}}}
  concept: {label: Concept, id_prefix: CON, properties: {status: {type: concept_status}}}
relations:
  caused-by: {label: caused by, from: [ticket], to: [concept]}
`))
	require.NoError(t, err)
	specs := queryplan.TraversalIndexSpecs(compileScope(t, `related(entity, 'caused-by', { status = 'rare' })`),
		meta, "ticket")
	require.Len(t, specs, 1)
	require.Equal(t, "concept", specs[0].Type)
	reconcile(t, s, specs)

	seed(t, s, func(v store.Store) {
		for i := range 500 {
			e := entity.New(fmt.Sprintf("CON-%06d", i), "concept")
			e.Properties["status"] = map[bool]string{true: "rare", false: "active"}[i == 499]
			mustCreate(t, v, e)
		}
		for i := range 2000 {
			id := fmt.Sprintf("TKT-%06d", i)
			mustCreate(t, v, entity.New(id, "ticket"))
			mustRelate(t, v, id, "caused-by", fmt.Sprintf("CON-%06d", i%500))
		}
	})
	plan := explain(t, s, store.GraphQuery{
		EntityType: "ticket",
		HasOutbound: &store.RelationPredicate{
			OfTypes: []string{"caused-by"},
			EndpointMatch: &store.EndpointPredicate{
				EntityType: "concept",
				Props:      []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "rare", Scalar: true}},
			},
		},
	})
	requireNoTableScan(t, plan)
}

// The incoming-hop twin: the endpoint is the relation's FROM side. The
// MatchingIDs statement a query scope issues for one page must not scan a
// table either.
func TestInboundEndpointMatchExplainIsIndexOnly(t *testing.T) {
	s := open(t)
	meta, err := metamodel.Parse([]byte(`version: "1.0"
namespace: https://example.org/test#
types:
  ticket_status: {values: [active, rare]}
entities:
  ticket: {label: Ticket, id_prefix: TKT, properties: {status: {type: ticket_status}}}
  feature: {label: Feature, id_prefix: FEAT, properties: {title: {type: string}}}
relations:
  implements: {label: implements, from: [ticket], to: [feature], inverse: implementedBy}
`))
	require.NoError(t, err)
	specs := queryplan.TraversalIndexSpecs(compileScope(t, `related(entity, 'implementedBy', { status = 'rare' })`),
		meta, "feature")
	require.Len(t, specs, 1)
	require.Equal(t, "ticket", specs[0].Type)
	reconcile(t, s, specs)

	seed(t, s, func(v store.Store) {
		for i := range 500 {
			mustCreate(t, v, entity.New(fmt.Sprintf("FEAT-%06d", i), "feature"))
		}
		for i := range 2000 {
			id := fmt.Sprintf("TKT-%06d", i)
			e := entity.New(id, "ticket")
			e.Properties["status"] = map[bool]string{true: "rare", false: "active"}[i == 1999]
			mustCreate(t, v, e)
			mustRelate(t, v, id, "implements", fmt.Sprintf("FEAT-%06d", i%500))
		}
	})
	q := store.GraphQuery{
		EntityType: "feature",
		HasInbound: &store.RelationPredicate{
			OfTypes: []string{"implements"},
			EndpointMatch: &store.EndpointPredicate{
				EntityType: "ticket",
				Props:      []store.PropPredicate{{Property: "status", Op: store.PropEqual, Value: "rare", Scalar: true}},
			},
		},
	}
	requireNoTableScan(t, explain(t, s, q))

	page := make([]string, 50)
	for i := range page {
		page[i] = fmt.Sprintf("FEAT-%06d", i)
	}
	plan, err := s.ExplainMatchingIDs(context.Background(), q, page)
	require.NoError(t, err)
	t.Logf("MatchingIDs plan:\n%s", plan)
	requireNoTableScan(t, plan)
	// A walk of the type index is a SEARCH, so the scan check alone misses it.
	require.Contains(t, strings.SplitN(plan, "\n", 2)[0], "sqlite_autoindex_entities_1 (id=?",
		"MatchingIDs is not driven by the page's id list")
}

// The related(entity, rel, { id = current_user.id }) shape (TKT-NXELMW): the
// bound user id is an inbound Endpoints entry and the traversed type an
// EndpointMatch. Neither the full query nor a page's MatchingIDs may scan a
// table, and no derived index is needed for it.
func TestInboundNamedEndpointExplainIsIndexOnly(t *testing.T) {
	s := open(t)
	seed(t, s, func(v store.Store) {
		for i := range 50 {
			mustCreate(t, v, entity.New(fmt.Sprintf("PER-%06d", i), "persoon"))
		}
		for i := range 2000 {
			id := fmt.Sprintf("TAAK-%06d", i)
			mustCreate(t, v, entity.New(id, "taak"))
			mustRelate(t, v, fmt.Sprintf("PER-%06d", i%50), "verantwoordelijk_voor", id)
		}
	})
	q := store.GraphQuery{
		EntityType: "taak",
		HasInbound: &store.RelationPredicate{
			OfTypes:       []string{"verantwoordelijk_voor"},
			Endpoints:     []string{"PER-000007"},
			EndpointMatch: &store.EndpointPredicate{EntityType: "persoon"},
		},
	}
	requireNoTableScan(t, explain(t, s, q))

	page := make([]string, 50)
	for i := range page {
		page[i] = fmt.Sprintf("TAAK-%06d", i)
	}
	plan, err := s.ExplainMatchingIDs(context.Background(), q, page)
	require.NoError(t, err)
	t.Logf("MatchingIDs plan:\n%s", plan)
	requireNoTableScan(t, plan)
}

// requireNoTableScan fails on a plan line that scans a table. Scans of a
// bound json_each list and of a CTE's own rows are allowed: both are bounded
// by the query's inputs, not by the store's size.
func requireNoTableScan(t *testing.T, plan string) {
	t.Helper()
	for line := range strings.SplitSeq(plan, "\n") {
		line = strings.TrimSpace(line)
		allowed := strings.Contains(line, "json_each") || strings.Contains(line, "closure") || line == "SCAN c"
		if !strings.HasPrefix(line, "SCAN ") || allowed {
			continue
		}
		t.Fatalf("plan scans a table (%q):\n%s", line, plan)
	}
}

func compileScope(t *testing.T, src string) *predicate.Program {
	t.Helper()
	env := predicate.NewEnv()
	require.NoError(t, env.DeclareVar("entity", predicate.RecordType{}))
	prog, err := predicate.Compile(env, src)
	require.NoError(t, err)
	return prog
}

// An entity-inheritance closure for one page starts from the page's ids, not
// from every entity of the type.
func TestMatchingIDsEntityClosureSeedsFromThePage(t *testing.T) {
	s := open(t)
	seed(t, s, func(v store.Store) {
		mustCreate(t, v, entity.New("alice", "person"))
		for i := range 2000 {
			id := fmt.Sprintf("ITEM-%06d", i)
			mustCreate(t, v, entity.New(id, "item"))
			if i > 0 {
				mustRelate(t, v, id, "partOf", fmt.Sprintf("ITEM-%06d", i-1))
			}
		}
		mustRelate(t, v, "alice", "owns", "ITEM-000000")
	})
	q := store.GraphQuery{
		EntityType: "item",
		HasInbound: &store.RelationPredicate{
			Endpoints: []string{"alice"}, OfTypes: []string{"owns"},
			EntityInheritThrough: []string{"partOf"}, EntityDepth: 5,
		},
	}
	page := []string{"ITEM-000003", "ITEM-000009"}
	plan, err := s.ExplainMatchingIDs(context.Background(), q, page)
	require.NoError(t, err)
	t.Logf("plan:\n%s", plan)
	require.NotContains(t, plan, "entities_type_idx", "the closure seeds from the whole type")
	requireNoTableScan(t, plan)

	got, err := s.MatchingIDs(context.Background(), q, page)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"ITEM-000003": true, "ITEM-000009": false}, got)
}
