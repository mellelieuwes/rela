package storetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// RunEndpointMatchTests is the conformance suite for
// [store.RelationPredicate.EndpointMatch] — filtering the candidate set by the
// TYPE and PROPERTIES of the entity on the far side of a relation, rather than
// by endpoint id (TKT-RELTRV).
//
// The scenarios that matter to a backend author, beyond the happy path:
//
//   - a dangling edge (endpoint row absent) is NOT a match, so a pushed-down
//     JOIN and a Go-side lookup agree
//   - EndpointMatch composes with Endpoints as a CONJUNCTION, not a widening
//   - Negate inverts the whole predicate including the endpoint filter
//   - a chained hop resolves against the endpoint, not the original candidate
//
// Run via [RunAll].
func RunEndpointMatchTests(t *testing.T, f Factory) {
	t.Helper()

	// seed builds: TKT-1 -caused-by-> CON-open, TKT-2 -caused-by-> CON-done,
	// TKT-3 -caused-by-> MISSING (dangling).
	seed := func(t *testing.T, s store.Store) {
		t.Helper()
		seedGraphQueryEntities(t, s, "ticket", "TKT-1", "TKT-2", "TKT-3")
		seedEntityWithProps(t, s, "concept", "CON-open", map[string]any{"status": "open"})
		seedEntityWithProps(t, s, "concept", "CON-done", map[string]any{"status": "done"})
		mustRel(t, s, "TKT-1", "caused-by", "CON-open")
		mustRel(t, s, "TKT-2", "caused-by", "CON-done")
		mustRel(t, s, "TKT-3", "caused-by", "CON-missing")
	}

	t.Run("Props_filters_by_endpoint_property", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"},
				EndpointMatch: &store.EndpointPredicate{
					Props: []store.PropPredicate{
						{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true},
					},
				},
			},
		})
		require.Equal(t, []string{"TKT-1"}, got)
	})

	t.Run("EntityType_resolves_a_union_target", func(t *testing.T) {
		s := f(t)
		seedGraphQueryEntities(t, s, "ticket", "TKT-1", "TKT-2")
		seedEntityWithProps(t, s, "concept", "CON-1", map[string]any{"status": "open"})
		seedEntityWithProps(t, s, "decision", "DEC-1", map[string]any{"status": "open"})
		mustRel(t, s, "TKT-1", "caused-by", "CON-1")
		mustRel(t, s, "TKT-2", "caused-by", "DEC-1")

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes:       []string{"caused-by"},
				EndpointMatch: &store.EndpointPredicate{EntityType: "decision"},
			},
		})
		require.Equal(t, []string{"TKT-2"}, got)
	})

	t.Run("dangling_edge_is_not_a_match", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// TKT-3's endpoint row does not exist. An any-type, any-property
		// endpoint match must still exclude it: a pushed-down INNER JOIN
		// drops the row, and the Go path must agree.
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes:       []string{"caused-by"},
				EndpointMatch: &store.EndpointPredicate{},
			},
		})
		require.Equal(t, []string{"TKT-1", "TKT-2"}, got)
	})

	t.Run("composes_with_Endpoints_as_conjunction", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// Endpoints names CON-done; the property filter names status=open.
		// Nothing satisfies both, so the result is empty — if the two were
		// OR-ed (or either were ignored) this would return a row.
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes:   []string{"caused-by"},
				Endpoints: []string{"CON-done"},
				EndpointMatch: &store.EndpointPredicate{
					Props: []store.PropPredicate{
						{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true},
					},
				},
			},
		})
		require.Empty(t, got)
	})

	t.Run("Negate_inverts_including_the_endpoint_filter", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// "has no caused-by edge to an open concept" — TKT-2 (edge to a done
		// concept) and TKT-3 (dangling) both qualify.
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"},
				Negate:  true,
				EndpointMatch: &store.EndpointPredicate{
					Props: []store.PropPredicate{
						{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true},
					},
				},
			},
		})
		require.Equal(t, []string{"TKT-2", "TKT-3"}, got)
	})

	t.Run("chained_hop_resolves_against_the_endpoint", func(t *testing.T) {
		s := f(t)
		// TKT-1 -caused-by-> CON-1 -owned-by-> alice
		// TKT-2 -caused-by-> CON-2 -owned-by-> bob
		seedGraphQueryEntities(t, s, "ticket", "TKT-1", "TKT-2")
		seedGraphQueryEntities(t, s, "concept", "CON-1", "CON-2")
		seedGraphQueryEntities(t, s, "person", "alice", "bob")
		mustRel(t, s, "TKT-1", "caused-by", "CON-1")
		mustRel(t, s, "TKT-2", "caused-by", "CON-2")
		mustRel(t, s, "CON-1", "owned-by", "alice")
		mustRel(t, s, "CON-2", "owned-by", "bob")

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"},
				EndpointMatch: &store.EndpointPredicate{
					EntityType: "concept",
					HasOutbound: &store.RelationPredicate{
						OfTypes:   []string{"owned-by"},
						Endpoints: []string{"alice"},
					},
				},
			},
		})
		require.Equal(t, []string{"TKT-1"}, got)
	})

	// The ACL folds its read query into an endpoint match, and that query
	// carries InheritThrough / EntityInheritThrough whenever the policy
	// declares role inheritance. pgstore's nested emitter cannot express them,
	// so BOTH backends must refuse rather than one silently ignoring them —
	// an authorization predicate that evaporates on one backend is the defect
	// this case exists to catch.
	t.Run("nested_inheritance_expansion_is_refused_by_every_backend", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		err := graphQueryErr(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"},
				EndpointMatch: &store.EndpointPredicate{
					EntityType: "concept",
					HasInbound: &store.RelationPredicate{
						Endpoints:      []string{"alice"},
						OfTypes:        []string{"owns"},
						InheritThrough: []string{"member-of"},
						Depth:          3,
					},
				},
			},
		})
		require.Error(t, err, "a nested inheritance expansion must be refused, not silently ignored")
	})

	// A NEGATED predicate inverts everything beneath it, so any guard that
	// renders an arm unsatisfiable instead of refusing would turn "has no
	// path to X" into "match everything". Pinned because a depth bound was
	// briefly implemented that way.
	t.Run("negated_chained_hop", func(t *testing.T) {
		s := f(t)
		// TKT-1 -> CON-open -owned-by-> alice ; TKT-2 -> CON-done (no owner)
		seed(t, s)
		mustRel(t, s, "CON-open", "owned-by", "alice")

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"},
				Negate:  true,
				EndpointMatch: &store.EndpointPredicate{
					EntityType: "concept",
					HasOutbound: &store.RelationPredicate{
						OfTypes: []string{"owned-by"}, Endpoints: []string{"alice"},
					},
				},
			},
		})
		// Only TKT-1 reaches an alice-owned concept, so the negation keeps the
		// other two. Critically it must NOT be all three.
		require.Equal(t, []string{"TKT-2", "TKT-3"}, got)
	})

	// The nesting bound must REFUSE, not render the too-deep arm
	// unsatisfiable: under an outer Negate an unsatisfiable arm inverts to
	// "match everything", turning a DoS guard into a row-gate bypass.
	t.Run("excessive_nesting_is_refused", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		inner := &store.EndpointPredicate{EntityType: "concept"}
		for range 12 {
			inner = &store.EndpointPredicate{
				EntityType:  "concept",
				HasOutbound: &store.RelationPredicate{OfTypes: []string{"caused-by"}, EndpointMatch: inner},
			}
		}
		err := graphQueryErr(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"caused-by"}, EndpointMatch: inner,
			},
		})
		require.Error(t, err, "nesting past the cap must be refused")
	})

	runInboundEndpointMatchTests(t, f)

	t.Run("nil_EndpointMatch_is_unconstrained", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// The pre-existing shape: a nil match must not start excluding the
		// dangling edge, or adding the field would change existing queries.
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType:  "ticket",
			HasOutbound: &store.RelationPredicate{OfTypes: []string{"caused-by"}},
		})
		require.Equal(t, []string{"TKT-1", "TKT-2", "TKT-3"}, got)
	})
}

// runInboundEndpointMatchTests covers an endpoint match reached by walking an
// edge BACKWARDS (TKT-CXQEV0): the candidate is the relation's TO side and the
// endpoint filtered on is its FROM side. Every backend takes the direction as
// a parameter, so these pin that none of them assumes the outgoing shape.
func runInboundEndpointMatchTests(t *testing.T, f Factory) {
	t.Helper()

	// TKT-open -implements-> FEAT-1, TKT-done -implements-> FEAT-2,
	// TKT-missing -implements-> FEAT-3 is dangling: its FROM row is never
	// written. FEAT-4 has no incoming edge. FEAT-1 -requires-> CON-1.
	seed := func(t *testing.T, s store.Store) {
		t.Helper()
		seedEntityWithProps(t, s, "ticket", "TKT-open", map[string]any{"status": "open"})
		seedEntityWithProps(t, s, "ticket", "TKT-done", map[string]any{"status": "done"})
		seedGraphQueryEntities(t, s, "feature", "FEAT-1", "FEAT-2", "FEAT-3", "FEAT-4")
		seedGraphQueryEntities(t, s, "concept", "CON-1", "CON-2")
		mustRel(t, s, "TKT-open", "implements", "FEAT-1")
		mustRel(t, s, "TKT-done", "implements", "FEAT-2")
		mustRel(t, s, "TKT-missing", "implements", "FEAT-3")
		mustRel(t, s, "FEAT-1", "requires", "CON-1")
		mustRel(t, s, "FEAT-2", "requires", "CON-2")
	}
	openTicket := func() *store.EndpointPredicate {
		return &store.EndpointPredicate{
			EntityType: "ticket",
			Props: []store.PropPredicate{
				{Property: "status", Op: store.PropEqual, Value: "open", Scalar: true},
			},
		}
	}

	t.Run("HasInbound_filters_by_the_FROM_endpoint", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "feature",
			HasInbound: &store.RelationPredicate{OfTypes: []string{"implements"}, EndpointMatch: openTicket()},
		})
		// FEAT-3's edge has no FROM row, so it cannot match: a pushed-down
		// INNER JOIN drops it, and the Go path must agree.
		require.Equal(t, []string{"FEAT-1"}, got)
	})

	t.Run("HasInbound_negated", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "feature",
			HasInbound: &store.RelationPredicate{
				OfTypes: []string{"implements"}, Negate: true, EndpointMatch: openTicket(),
			},
		})
		require.Equal(t, []string{"FEAT-2", "FEAT-3", "FEAT-4"}, got)
	})

	// The related(entity, rel, { id = current_user.id }) shape (TKT-NXELMW):
	// the bound user id arrives as Endpoints on the FROM side, and the hop's
	// target type as EndpointMatch.EntityType. The two are a conjunction.
	namedTicket := func(id string, negate bool) *store.RelationPredicate {
		return &store.RelationPredicate{
			OfTypes: []string{"implements"}, Endpoints: []string{id}, Negate: negate,
			EndpointMatch: &store.EndpointPredicate{EntityType: "ticket"},
		}
	}
	for _, tc := range []struct {
		name, endpoint string
		negate         bool
		want           []string
	}{
		{"HasInbound_named_endpoint_of_type", "TKT-done", false, []string{"FEAT-2"}},
		{"HasInbound_named_endpoint_of_type_negated", "TKT-done", true, []string{"FEAT-1", "FEAT-3", "FEAT-4"}},
		// The id names a row of another type: the type check still applies.
		{"HasInbound_named_endpoint_of_other_type", "FEAT-1", false, nil},
		// The id names the dangling FROM of FEAT-3's edge: no row, no match.
		{"HasInbound_named_dangling_endpoint", "TKT-missing", false, nil},
		{"HasInbound_named_dangling_endpoint_negated", "TKT-missing", true,
			[]string{"FEAT-1", "FEAT-2", "FEAT-3", "FEAT-4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := f(t)
			seed(t, s)
			got := runGraphQuery(t, s, store.GraphQuery{
				EntityType: "feature",
				HasInbound: namedTicket(tc.endpoint, tc.negate),
			})
			if tc.want == nil {
				require.Empty(t, got)
				return
			}
			require.Equal(t, tc.want, got)
		})
	}

	t.Run("chain_inbound_then_inbound", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// concept <-requires- feature <-implements- open ticket
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "concept",
			HasInbound: &store.RelationPredicate{
				OfTypes: []string{"requires"},
				EndpointMatch: &store.EndpointPredicate{
					EntityType: "feature",
					HasInbound: &store.RelationPredicate{
						OfTypes: []string{"implements"}, EndpointMatch: openTicket(),
					},
				},
			},
		})
		require.Equal(t, []string{"CON-1"}, got)
	})

	t.Run("chain_outbound_then_inbound", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		// ticket -implements-> feature <-implements- open ticket: the tickets
		// that share a feature with an open ticket (including itself).
		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"implements"},
				EndpointMatch: &store.EndpointPredicate{
					EntityType: "feature",
					HasInbound: &store.RelationPredicate{
						OfTypes: []string{"implements"}, EndpointMatch: openTicket(),
					},
				},
			},
		})
		require.Equal(t, []string{"TKT-open"}, got)
	})

	// A caller traversal answers from the DEFAULT state only: the endpoint's
	// default face and default-tailed edges. A named face belongs to a world
	// the reader may not be granted, so letting it satisfy the filter would
	// disclose draft content through which candidates match (TKT-CXQEV0
	// design review). The backends disagreed here before: postgres joined any
	// face of the endpoint, the Go path read only the default one.
	t.Run("named_face_of_the_endpoint_does_not_match", func(t *testing.T) {
		s := f(t)
		seedGraphQueryEntities(t, s, "feature", "FEAT-1")
		seedEntityWithProps(t, s, "ticket", "TKT-1", map[string]any{"status": "done"})
		draft := entity.New("TKT-1", "ticket")
		draft.Face = draftFace(t)
		draft.Properties["status"] = "open"
		require.NoError(t, s.CreateEntity(ctx(), draft))
		mustRel(t, s, "TKT-1", "implements", "FEAT-1")

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "feature",
			HasInbound: &store.RelationPredicate{OfTypes: []string{"implements"}, EndpointMatch: openTicket()},
		})
		require.Empty(t, got, "only the draft face is open; the default face is done")
	})

	t.Run("named_face_tailed_edge_does_not_match", func(t *testing.T) {
		s := f(t)
		seedGraphQueryEntities(t, s, "feature", "FEAT-1")
		seedEntityWithProps(t, s, "ticket", "TKT-1", map[string]any{"status": "open"})
		draft := entity.New("TKT-1", "ticket")
		draft.Face = draftFace(t)
		draft.Properties["status"] = "open"
		require.NoError(t, s.CreateEntity(ctx(), draft))
		// Only the draft state implements the feature.
		_, err := s.CreateRelation(ctx(), "TKT-1", "implements", "FEAT-1",
			&store.RelationData{FromFace: draftFace(t)})
		require.NoError(t, err)

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "feature",
			HasInbound: &store.RelationPredicate{OfTypes: []string{"implements"}, EndpointMatch: openTicket()},
		})
		require.Empty(t, got, "the only edge is tailed on the draft face")
	})

	// The candidate side: an OUTGOING hop reads the candidate's own edges,
	// and only its default-state tail counts.
	t.Run("outbound_named_face_tailed_edge_does_not_match", func(t *testing.T) {
		s := f(t)
		seedGraphQueryEntities(t, s, "feature", "FEAT-1")
		for _, id := range []string{"TKT-1", "TKT-2"} {
			seedEntityWithProps(t, s, "ticket", id, map[string]any{"status": "open"})
		}
		draft := entity.New("TKT-1", "ticket")
		draft.Face = draftFace(t)
		require.NoError(t, s.CreateEntity(ctx(), draft))
		_, err := s.CreateRelation(ctx(), "TKT-1", "implements", "FEAT-1",
			&store.RelationData{FromFace: draftFace(t)})
		require.NoError(t, err)
		mustRel(t, s, "TKT-2", "implements", "FEAT-1")

		got := runGraphQuery(t, s, store.GraphQuery{
			EntityType: "ticket",
			HasOutbound: &store.RelationPredicate{
				OfTypes: []string{"implements"}, EndpointMatch: &store.EndpointPredicate{EntityType: "feature"},
			},
		})
		require.Equal(t, []string{"TKT-2"}, got, "TKT-1's only edge is tailed on its draft face")
	})

	// A nested hop is pinned to the default state too, not only the first.
	t.Run("chain_second_hop_named_face_tailed_edge_does_not_match", func(t *testing.T) {
		s := f(t)
		seedGraphQueryEntities(t, s, "feature", "FEAT-1")
		seedEntityWithProps(t, s, "ticket", "TKT-1", map[string]any{"status": "open"})
		seedGraphQueryEntities(t, s, "user", "USR-1")
		draft := entity.New("USR-1", "user")
		draft.Face = draftFace(t)
		require.NoError(t, s.CreateEntity(ctx(), draft))
		mustRel(t, s, "TKT-1", "implements", "FEAT-1")
		_, err := s.CreateRelation(ctx(), "USR-1", "reports", "TKT-1",
			&store.RelationData{FromFace: draftFace(t)})
		require.NoError(t, err)

		chain := func() store.GraphQuery {
			return store.GraphQuery{
				EntityType: "feature",
				HasInbound: &store.RelationPredicate{
					OfTypes: []string{"implements"},
					EndpointMatch: &store.EndpointPredicate{
						EntityType: "ticket",
						HasInbound: &store.RelationPredicate{
							OfTypes: []string{"reports"}, EndpointMatch: &store.EndpointPredicate{EntityType: "user"},
						},
					},
				},
			}
		}
		require.Empty(t, runGraphQuery(t, s, chain()), "the only reports edge is tailed on the draft face")

		mustRel(t, s, "USR-1", "reports", "TKT-1")
		require.Equal(t, []string{"FEAT-1"}, runGraphQuery(t, s, chain()), "a default-tailed edge matches")
	})

	t.Run("MatchingIDs_answers_an_inbound_match", func(t *testing.T) {
		s := f(t)
		seed(t, s)
		got, err := s.MatchingIDs(ctx(), store.GraphQuery{
			EntityType: "feature",
			HasInbound: &store.RelationPredicate{OfTypes: []string{"implements"}, EndpointMatch: openTicket()},
		}, []string{"FEAT-1", "FEAT-2", "FEAT-3", "FEAT-4"})
		require.NoError(t, err)
		require.True(t, got["FEAT-1"], "FEAT-1 has an open implementing ticket")
		for _, id := range []string{"FEAT-2", "FEAT-3", "FEAT-4"} {
			require.False(t, got[id], "%s must not match", id)
		}
	})
}

// draftFace is the named face the endpoint-match tests put out of reach.
func draftFace(t *testing.T) entity.Face {
	t.Helper()
	p, err := entity.ParseFace("draft")
	require.NoError(t, err)
	return p
}

// graphQueryErr drains a GraphQuery and returns the first error, or nil.
// Unlike runGraphQuery it does not fail the test — callers asserting a
// REFUSAL need the error rather than a fatal.
func graphQueryErr(t *testing.T, s store.Store, q store.GraphQuery) error {
	t.Helper()
	for _, err := range s.GraphQuery(context.Background(), q) {
		if err != nil {
			return err
		}
	}
	return nil
}
