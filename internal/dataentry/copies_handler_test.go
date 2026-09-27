package dataentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	v1 "github.com/Sourcehaven-BV/rela/internal/apiwire/v1"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// stubCopyService stands in for the manager. It records what it was asked so
// the tests can assert the handler does not pre-empt it.
type stubCopyService struct {
	result *entitymanager.CopyResult
	err    error

	invokeCalls int
	gotReq      entitymanager.CopyRequest
}

func (s *stubCopyService) CopyState(
	_ context.Context, req entitymanager.CopyRequest,
) (*entitymanager.CopyResult, error) {
	s.invokeCalls++
	s.gotReq = req
	return s.result, s.err
}

func newCopyTestHandler(t *testing.T, svc copyService) *copiesHandler {
	t.Helper()
	h, err := newCopiesHandler(svc)
	if err != nil {
		t.Fatalf("newCopiesHandler: %v", err)
	}
	return h
}

func serveCopies(h *copiesHandler, method, target, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, http.NoBody)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.handleV1Copies(rec, r)
	return rec
}

// TestCopiesHandler_RejectsNilService pins the constructor rule.
//
// A nil service must fail at CONSTRUCTION, not per request, because an empty
// offer list is a legitimate domain answer — a face with no declared copies
// returns exactly that. So a nil-and-skip design would render a wiring bug as
// "the promote button never appears", sending someone to hunt through
// schema.yaml for a definition that is correctly declared.
func TestCopiesHandler_RejectsNilService(t *testing.T) {
	t.Parallel()
	if _, err := newCopiesHandler(nil); err == nil {
		t.Error("a nil copy service must be rejected at construction — a " +
			"wiring failure that renders as a valid domain answer is the " +
			"hardest kind to diagnose")
	}
}

// TestCopiesHandler_InvokeDoesNotPreEmptTheKernel is the load-bearing
// authorization test.
//
// The handler must NOT check the guard itself. Two authorization sites that
// can disagree is strictly worse than one, because the divergence is
// invisible — both look plausible. So the assertion is that the kernel is
// ALWAYS consulted, including for a request the handler might have been
// tempted to reject early.
//
// Mutation-checked: adding any pre-flight allow/deny check to invoke() fails
// this, because the stub's call count drops to zero.
func TestCopiesHandler_InvokeDoesNotPreEmptTheKernel(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{err: &acl.ForbiddenError{Decision: acl.Decision{
		RuleKind: "copy-guard", RuleID: "promote-page",
		Reason: "copy \"promote-page\" requires permission \"promote-page\" on \"PAGE-1\"",
	}}}
	h := newCopyTestHandler(t, svc)

	rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/promote-page",
		`{"source_id":"PAGE-1"}`)

	if svc.invokeCalls != 1 {
		t.Fatalf("the kernel must be consulted exactly once, even for a request "+
			"that will be denied — the handler must not pre-empt it; calls=%d",
			svc.invokeCalls)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("a kernel ForbiddenError maps to 403; got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "promote-page") {
		t.Errorf("a 403 should name the missing permission — a copy definition "+
			"and its guard are operator config, not secrets; got %s", rec.Body)
	}
}

// TestCopiesHandler_MissingSourceIs404Indistinguishable is the existence-oracle
// test.
//
// The kernel folds a DENIED read on the source into ErrCopySourceMissing
// precisely so a caller cannot tell "you may not read this" from "this does
// not exist". The handler must preserve that: a 403 here — however much more
// helpful — would undo the read gate's work.
//
// It asserts BOTH the status and that the body carries no distinguishing
// detail, because a 404 whose body names the source would leak the same bit
// the status was careful not to.
func TestCopiesHandler_MissingSourceIs404Indistinguishable(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{
		err: fmt.Errorf("%w: PAGE-SECRET", entitymanager.ErrCopySourceMissing),
	}
	h := newCopyTestHandler(t, svc)

	rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/promote-page",
		`{"source_id":"PAGE-SECRET"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("a missing-or-denied source must be 404, never 403: the kernel "+
			"collapses the two so a caller cannot tell them apart; got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "PAGE-SECRET") {
		t.Errorf("the 404 body must not name the source — that would leak "+
			"through the body the bit the status withheld; got %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), entityNotFoundTitle) {
		t.Errorf("the 404 must use the shared not-found title so it is "+
			"byte-comparable with every other read-path 404; got %s", rec.Body)
	}
}

// TestCopiesHandler_KernelRefusalsAre422 covers the third arm: a coherent
// request this surface cannot satisfy — the kernel's request-shape sentinels,
// whose text names config and caller-supplied ids and is safe to echo.
func TestCopiesHandler_KernelRefusalsAre422(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{
		entitymanager.ErrUnknownCopy, entitymanager.ErrCopyTargetRequired,
		entitymanager.ErrCopyTargetNotAllowed, entitymanager.ErrCopyTargetTypeMismatch,
	} {
		svc := &stubCopyService{err: fmt.Errorf("%w: %q", sentinel, "nope")}
		h := newCopyTestHandler(t, svc)
		rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/nope", `{"source_id":"PAGE-1"}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%v: a request-shape refusal maps to 422; got %d", sentinel, rec.Code)
		}
	}
}

// TestCopiesHandler_OwnedTargetIs422ExternallyOwned: a copy the kernel refuses
// because it would change a twin-owned field of the target answers the same
// 422 externally_owned as every other write, not a 500.
func TestCopiesHandler_OwnedTargetIs422ExternallyOwned(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{err: fmt.Errorf("copy: %w", &entitymanager.TheirsWriteError{
		Type: "ticket", ID: "TKT-2", Fields: []string{"title"}, Systems: []string{"basecamp"},
	})}
	h := newCopyTestHandler(t, svc)
	rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/spawn",
		`{"source_id":"TKT-1","target_id":"TKT-2"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "/errors/externally_owned") ||
		!strings.Contains(rec.Body.String(), "owned by basecamp") {

		t.Errorf("body = %s, want the externally_owned problem naming basecamp", rec.Body)
	}
}

// TestCopiesHandler_InfrastructureErrorsAre500AndGeneric pins the fourth arm.
// A store fault wrapped by the kernel used to arrive as a 422 whose detail was
// the raw error text — on pgstore, SQL error text — exactly what every other
// handler here refuses to emit. The status is a 500 (the request was fine, the
// server was not) and the detail is generic.
func TestCopiesHandler_InfrastructureErrorsAre500AndGeneric(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{err: errors.New("pq: relation \"entities\" does not exist")}
	h := newCopyTestHandler(t, svc)

	rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/promote", `{"source_id":"PAGE-1"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("an infrastructure failure is a 500, not a 422; got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") || strings.Contains(rec.Body.String(), "pq:") {
		t.Errorf("the body must not echo backend error text; got %s", rec.Body)
	}
}

// TestCopiesHandler_InvokesByNameOnly pins the transforms-registry precedent:
// the request names a DEFINITION, and the name comes from the PATH.
//
// If a caller could describe a copy, they could describe one whose guard is
// convenient and the guard system would be decorative. The body carries only
// entity ids, which is why entitymanager.CopyRequest is three strings.
func TestCopiesHandler_InvokesByNameOnly(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{result: &entitymanager.CopyResult{
		Definition: "promote-page",
		Entity:     &entity.Entity{ID: "PAGE-1", Type: "page", Face: entity.Face("published")},
		Created:    true,
	}}
	h := newCopyTestHandler(t, svc)

	rec := serveCopies(h, http.MethodPost, "/api/v1/_copies/promote-page",
		`{"source_id":"PAGE-1","fields":{"title":"injected"},"guard":null}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	// The definition came from the PATH; unknown body keys were ignored rather
	// than shaping the copy.
	if svc.gotReq.Definition != "promote-page" {
		t.Errorf("definition must come from the path; got %q", svc.gotReq.Definition)
	}
	if svc.gotReq.SourceID != "PAGE-1" {
		t.Errorf("source_id = %q", svc.gotReq.SourceID)
	}

	var got v1.CopyResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Face != "published" || !got.Created {
		t.Errorf("the result must report the written face and whether it was "+
			"created; got %+v", got)
	}
}

// TestCopiesHandler_MethodRouting pins the surface's shape: a list is a GET on
// the collection, an invoke is a POST to a NAME. A POST to the collection has
// no definition to invoke, and a GET on a name is not a read of anything.
func TestCopiesHandler_MethodRouting(t *testing.T) {
	t.Parallel()
	h := newCopyTestHandler(t, &stubCopyService{})

	for _, tc := range []struct {
		name, method, target string
		want                 int
	}{
		{"POST to collection", http.MethodPost, "/api/v1/_copies", http.StatusMethodNotAllowed},
		// GET is gone entirely: offers ride the entity response alongside
		// `_actions` rather than living behind a second endpoint. Both the
		// collection and a named definition refuse it.
		{"GET the collection", http.MethodGet, "/api/v1/_copies", http.StatusMethodNotAllowed},
		{"GET a name", http.MethodGet, "/api/v1/_copies/promote-page", http.StatusMethodNotAllowed},
		{"DELETE", http.MethodDelete, "/api/v1/_copies/promote-page", http.StatusMethodNotAllowed},
		{"OPTIONS", http.MethodOptions, "/api/v1/_copies", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := serveCopies(h, tc.method, tc.target, "").Code; got != tc.want {
				t.Errorf("%s %s: got %d, want %d", tc.method, tc.target, got, tc.want)
			}
		})
	}
}

// TestCopiesHandler_InvokeRejectsMalformedBody covers the wire-format errors,
// which are 400s rather than 422s: the request structure is broken, which is
// detectable without the metamodel (the DEC-HWZHA split).
func TestCopiesHandler_InvokeRejectsMalformedBody(t *testing.T) {
	t.Parallel()
	svc := &stubCopyService{}
	h := newCopyTestHandler(t, svc)

	if got := serveCopies(h, http.MethodPost, "/api/v1/_copies/x", `{not json`).Code; got != http.StatusBadRequest {
		t.Errorf("malformed JSON is a 400; got %d", got)
	}
	if got := serveCopies(h, http.MethodPost, "/api/v1/_copies/x", `{}`).Code; got != http.StatusBadRequest {
		t.Errorf("a missing source_id is a 400; got %d", got)
	}
	if svc.invokeCalls != 0 {
		t.Errorf("a malformed request must not reach the kernel; calls=%d", svc.invokeCalls)
	}
}

// TestCopyOffers_RideTheEntityResponse pins the design correction: offers
// arrive on the entity response alongside `_actions`, not from a second
// endpoint. An earlier revision shipped `GET /_copies?type=&face=&source_id=`,
// which made the client construct a lookup key for an affordance every other
// affordance in this app delivers inline.
//
// The three cases are the omitted-vs-empty contract computeCopyOffers
// documents: `_copies` is nil when nothing is wired or the query fails, and
// present (possibly empty) when the capability answered. The unit level can
// only exercise the accessor; TestCopyOffers_ReachTheWire covers the wiring.
func TestCopyOffers_RideTheEntityResponse(t *testing.T) {
	t.Parallel()
	app := newTestAppV1(t)
	e := &entity.Entity{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "t"}}

	// attach runs attachEntityAffordances with the given accessor on a copy
	// of the app's service, so subtests do not share mutable state.
	attach := func(copies copyOffersFunc) v1.Entity {
		svc := app.affordances
		svc.copies = copies
		var out v1.Entity
		svc.attachEntityAffordances(context.Background(), e, &out)
		// The attach ran: other affordances are present. Without this an
		// omission assertion would pass against a build that attached nothing.
		if out.FieldAffordances == nil {
			t.Fatal("precondition: attachEntityAffordances must have run")
		}
		return out
	}

	t.Run("omitted when the capability is not wired", func(t *testing.T) {
		t.Parallel()
		if out := attach(nil); out.Copies != nil {
			t.Errorf("`_copies` must be OMITTED when nothing is wired, not sent "+
				"empty — an empty list is a real answer, so sending one would "+
				"render a wiring gap as a domain fact; got %v", *out.Copies)
		}
	})

	t.Run("present, possibly empty, when wired", func(t *testing.T) {
		t.Parallel()
		out := attach(func(_ context.Context, _, _, _ string) ([]entitymanager.CopyOffer, error) {
			return []entitymanager.CopyOffer{{
				Name: "promote-ticket", Label: "Publish",
				TargetFace: "ticket@published", Allowed: true,
			}}, nil
		})
		if out.Copies == nil {
			t.Fatal("`_copies` must be present when the capability is wired")
		}
		got := *out.Copies
		if len(got) != 1 || got[0].Name != "promote-ticket" || got[0].Label != "Publish" {
			t.Errorf("the offer must reach the wire intact; got %+v", got)
		}
		if !got[0].Allowed {
			t.Error("the verdict must ride the offer — it is what Ruling 9's " +
				"no-permission-no-button reads")
		}
	})

	t.Run("omitted on a capability error, never silently empty", func(t *testing.T) {
		t.Parallel()
		out := attach(func(_ context.Context, _, _, _ string) ([]entitymanager.CopyOffer, error) {
			return nil, errors.New("backend down")
		})
		if out.Copies != nil {
			t.Errorf("a failure must omit `_copies` rather than emit `[]`, "+
				"which would read as \"no copies here\"; got %v", *out.Copies)
		}
	})
}

// copiesWireMeta declares one same-entity copy on page's draft face and a
// ticket type with none, so one app can serve both the populated and the
// present-but-empty shape of `_copies`.
const copiesWireMeta = `
version: "1"
entities:
  page:
    label: Page
    id_prefix: PAGE
    faces:
      draft: {}
      published: {}
    properties:
      title: {type: string}
  ticket:
    label: Ticket
    id_prefix: TKT
    properties:
      title: {type: string}
copies:
  promote-page:
    from: page@draft
    to: page@published
    fields: all
    label: Publish
    guard:
      permission: promote-page
`

// TestCopyOffers_ReachTheWire drives a GET through the production router on
// an app wired the way NewApp wires it (rebindApp calls the same wireCopies),
// and reads `_copies` off the JSON.
//
// It is the only test that can see the difference between "present and empty"
// and "omitted": the Go side spells both as a *[]CopyOffer, and only the encoded
// body shows whether the key was written. A ticket face declares no copies,
// so its `_copies` must be `[]` — the question was asked and the answer is
// none — while a page draft carries its promote offer with the verdict.
func TestCopyOffers_ReachTheWire(t *testing.T) {
	t.Parallel()
	meta, err := metamodel.Parse([]byte(copiesWireMeta))
	if err != nil {
		t.Fatalf("metamodel.Parse: %v", err)
	}
	app := newAppFromParts(&Config{App: AppConfig{Name: "Copies", Description: "x"}}, meta, newFixture())
	// `page` declares faces, so its draft is stored AT `draft` — there is no
	// zero-coordinate row to seed (BUG-HC6I2T).
	seedEntity(app, &entity.Entity{
		ID: "PAGE-1", Type: "page", Face: "draft", Properties: map[string]any{"title": "Draft"},
	})
	seedEntity(app, &entity.Entity{ID: "TKT-1", Type: "ticket", Properties: map[string]any{"title": "t"}})

	// rawCopies returns the `_copies` value as encoded, and whether the key
	// was present at all.
	rawCopies := func(t *testing.T, path string) ([]v1.CopyOffer, bool) {
		t.Helper()
		rec := doRequest(t, app, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		raw, ok := body["_copies"]
		if !ok {
			return nil, false
		}
		var offers []v1.CopyOffer
		if err := json.Unmarshal(raw, &offers); err != nil {
			t.Fatalf("decode _copies: %v", err)
		}
		return offers, true
	}

	t.Run("a face with a declared copy carries the offer and its verdict", func(t *testing.T) {
		t.Parallel()
		// Addressed by face: a faced type has no unsuffixed row to GET.
		offers, present := rawCopies(t, "/api/v1/pages/PAGE-1@draft")
		if !present {
			t.Fatal("`_copies` must be present on a per-entity response when the " +
				"capability is wired — the test rebind uses production wiring")
		}
		if len(offers) != 1 || offers[0].Name != "promote-page" || !offers[0].Allowed {
			t.Errorf("the draft face must offer promote-page as allowed; got %+v", offers)
		}
		if offers[0].Label != "Publish" || offers[0].TargetFace != "page@published" {
			t.Errorf("label and target must reach the wire intact; got %+v", offers[0])
		}
	})

	t.Run("a face with no declared copies carries an EMPTY list, not an omission", func(t *testing.T) {
		t.Parallel()
		offers, present := rawCopies(t, "/api/v1/tickets/TKT-1")
		if !present {
			t.Fatal("`_copies` must be present-but-empty when the capability is wired " +
				"and the face declares nothing: the question was asked")
		}
		if len(offers) != 0 {
			t.Errorf("ticket declares no copies; got %+v", offers)
		}
	})
}
