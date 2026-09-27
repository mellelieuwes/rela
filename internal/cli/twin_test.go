//go:build !postgres && !sqlite

// Twins run on the filesystem backends only in stage 1: a database build
// with pacts refuses to assemble (appbuild.buildTwins), so these end-to-end
// tests exist for the builds that can hold twins.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/appbuild"
	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/output"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/script"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/filetwins"
)

// scenarioTypeYAML declares a scenario type with no pact; twinSchema adds
// one, in which Basecamp owns status and the body and title stays rela's.
const scenarioTypeYAML = `version: "1.0"
entities:
  scenario:
    label: Scenario
    plural: scenarios
    id_prefix: "SC-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      status:
        type: enum
        values: [open, done]
      code:
        type: string
        unique: true
`

const twinSchema = scenarioTypeYAML + `    pacts:
      basecamp:
        scope: https://example.com/lists/1
        theirs: [status, code, body]
        instructions: Map todo.completed to status.
relations: {}
`

// twinProject is a real project on disk (the twin store is file-backed),
// discovered the way `rela` discovers it.
type twinProject struct {
	root string
	svc  *appbuild.Services
	w    *writeServices
}

func newTwinProject(t *testing.T, schema string) *twinProject {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"entities", "relations", ".rela"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "schema.yaml"), []byte(schema), 0o644))
	svc, err := appbuild.Discover(root, script.NewEngine())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	b, err := newCLIBundles(svc)
	require.NoError(t, err)
	return &twinProject{root: root, svc: svc, w: b.write}
}

func twinCtx() context.Context {
	return principal.With(context.Background(), principal.Principal{User: "agent", Tool: principal.ToolCLI})
}

func (p *twinProject) createScenario(t *testing.T, title string) string {
	t.Helper()
	e := entity.New("", "scenario")
	e.SetString("title", title)
	e.SetString("status", "open")
	res, err := p.svc.EntityManager().CreateEntity(twinCtx(), e, entity.CreateOptions{})
	require.NoError(t, err)
	return res.Entity.ID
}

func (p *twinProject) version(t *testing.T, id string) string {
	t.Helper()
	e, err := p.svc.Store().GetEntity(context.Background(), id)
	require.NoError(t, err)
	return string(store.VersionOf(e))
}

func (p *twinProject) remoteFile(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote.json")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

type twinRunner interface {
	Run(ctx context.Context, svc *writeServices) error
}

// runJSON runs cmd with -o json and decodes what it printed.
func (p *twinProject) runJSON(t *testing.T, cmd twinRunner, into any) {
	t.Helper()
	buf := withOutput(t, output.FormatJSON)
	require.NoError(t, cmd.Run(twinCtx(), p.w))
	require.NoError(t, json.Unmarshal(buf.Bytes(), into), "output: %s", buf.String())
}

// TestTwinCLI_SyncLoop drives the agent loop end to end through the
// commands: link, pending, pull, the ownership guard, pushed.
func TestTwinCLI_SyncLoop(t *testing.T) {
	p := newTwinProject(t, twinSchema)
	ctx := twinCtx()
	id := p.createScenario(t, "Checkout")
	untwinned := p.createScenario(t, "Refund")

	var linked map[string]any
	p.runJSON(t, &TwinLinkCmd{EntityID: id, System: "basecamp", ExternalID: "todo-1",
		URL: "https://example.com/todos/1"}, &linked)
	assert.Equal(t, "pending", linked["state"])
	assert.Equal(t, false, linked["has_base"])
	assert.Equal(t, map[string]any{"type": "scenario", "id": id}, linked["entity"])
	assert.NotContains(t, linked, "synced_at", "zero times are omitted")

	// Never synced: every ours field is to be pushed, nothing theirs.
	var pending []map[string]any
	p.runJSON(t, &TwinPendingCmd{System: "basecamp"}, &pending)
	require.Len(t, pending, 1)
	assert.Equal(t, []any{"never synced"}, pending[0]["reasons"])
	assert.Equal(t, p.version(t, id), pending[0]["version"])
	assert.Equal(t, []any{map[string]any{"field": "title", "base": nil, "local": "Checkout"}}, pending[0]["push_set"])
	assert.Equal(t, []any{}, pending[0]["findings"])

	// First pull: theirs fields are applied, the ours title agrees.
	remote := p.remoteFile(t, `{"properties": {"title": "Checkout", "status": "done"}, "body": "From Basecamp."}`)
	pull := &TwinPullCmd{System: "basecamp", ExternalID: "todo-1", Remote: remote,
		RemoteUpdatedAt: "2026-09-20T14:03:11Z"}
	var pulled map[string]any
	p.runJSON(t, pull, &pulled)
	assert.Equal(t, []any{"body", "status"}, pulled["applied"])
	assert.Equal(t, "in_sync", pulled["state"])
	assert.Equal(t, true, pulled["has_base"])
	assert.Equal(t, "2026-09-20T14:03:11Z", pulled["remote_updated_at"])
	stored, err := p.svc.Store().GetEntity(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "done", stored.GetString("status"))
	assert.Contains(t, stored.Content, "From Basecamp.")

	// The sync write is audited as the twin tool under the real user.
	assert.Contains(t, readAudit(t, p.root), `"tool":"twin"`)

	// An identical pull writes nothing.
	before := p.version(t, id)
	p.runJSON(t, pull, &pulled)
	assert.Equal(t, []any{}, pulled["applied"])
	assert.Equal(t, "in_sync", pulled["state"])
	assert.Equal(t, before, p.version(t, id), "an identical pull must not write the entity")

	// The guard: status belongs to Basecamp on the twinned entity only.
	_ = withOutput(t, output.FormatTable)
	err = (&UpdateCmd{ID: id, Status: "open"}).Run(ctx, p.w)
	var theirs *entitymanager.TheirsWriteError
	require.ErrorAs(t, err, &theirs)
	require.EqualError(t, err, "field status of "+id+" is owned by basecamp (Twin); change it there")
	require.NoError(t, (&UpdateCmd{ID: untwinned, Status: "done"}).Run(ctx, p.w))

	// A rela edit of an ours field puts the twin on the work list.
	require.NoError(t, (&UpdateCmd{ID: id, Title: "Checkout, expired card"}).Run(ctx, p.w))
	p.runJSON(t, &TwinPendingCmd{}, &pending)
	require.Len(t, pending, 1)
	assert.Equal(t, []any{"changed in rela"}, pending[0]["reasons"])
	assert.Equal(t, true, pending[0]["local_changed"])
	assert.Equal(t, []any{map[string]any{"field": "title", "base": "Checkout", "local": "Checkout, expired card"}},
		pending[0]["push_set"])
	version, _ := pending[0]["version"].(string)
	require.NotEmpty(t, version)

	// pushed is a compare-and-swap on the version pending listed.
	err = (&TwinPushedCmd{System: "basecamp", ExternalID: "todo-1", Version: before}).Run(ctx, p.w)
	require.ErrorIs(t, err, twins.ErrVersionConflict)
	assert.Contains(t, err.Error(), "run rela twin pending again")

	var pushed map[string]any
	p.runJSON(t, &TwinPushedCmd{System: "basecamp", ExternalID: "todo-1", Version: version}, &pushed)
	assert.Equal(t, "in_sync", pushed["state"])
	assert.Equal(t, "2026-09-20T14:03:11Z", pushed["remote_updated_at"], "pushed without the flag keeps the time")
	p.runJSON(t, &TwinPendingCmd{}, &pending)
	assert.Empty(t, pending)

	// A pull older than the last one is refused unless forced.
	stale := &TwinPullCmd{System: "basecamp", ExternalID: "todo-1", Remote: remote,
		RemoteUpdatedAt: "2026-09-19T00:00:00Z"}
	err = stale.Run(ctx, p.w)
	require.ErrorIs(t, err, twins.ErrStalePull)
	assert.Contains(t, err.Error(), "--force")
	stale.Force = true
	p.runJSON(t, stale, &pulled)
	assert.Equal(t, "2026-09-19T00:00:00Z", pulled["remote_updated_at"])

	// show reports the owned fields per system.
	var shown []map[string]any
	p.runJSON(t, &TwinShowCmd{EntityID: id}, &shown)
	require.Len(t, shown, 1)
	assert.Equal(t, []any{"body", "code", "status"}, shown[0]["owned_fields"])
}

// TestTwinCLI_RejectedPullSucceeds: a sync write rela refuses on its merits
// (here a unique property already taken) is a finding, not a failed command.
func TestTwinCLI_RejectedPullSucceeds(t *testing.T) {
	p := newTwinProject(t, twinSchema)
	ctx := twinCtx()
	id := p.createScenario(t, "Checkout")
	taken := p.createScenario(t, "Refund")
	_, err := p.svc.EntityManager().PatchEntity(ctx, taken, entity.Patch{Properties: map[string]any{"code": "A"}})
	require.NoError(t, err)
	buf := withOutput(t, output.FormatTable)
	require.NoError(t, (&TwinLinkCmd{EntityID: id, System: "basecamp", ExternalID: "todo-1",
		URL: "https://example.com/todos/1"}).Run(ctx, p.w))

	pull := &TwinPullCmd{System: "basecamp", ExternalID: "todo-1",
		Remote: p.remoteFile(t, `{"properties": {"code": "A"}}`), RemoteUpdatedAt: "2026-09-20T14:03:11Z"}
	require.NoError(t, pull.Run(ctx, p.w))
	assert.Contains(t, buf.String(), "rela refused the sync write")

	var listed []map[string]any
	p.runJSON(t, &TwinListCmd{State: "pending"}, &listed)
	require.Len(t, listed, 1)
	findings, _ := listed[0]["findings"].([]any)
	require.Len(t, findings, 1)
	assert.Equal(t, "rejected", findings[0].(map[string]any)["kind"])
}

// TestTwinCLI_ErrorsNameTheCause pins the operator-facing mapping of the
// twins failures a command can hit.
func TestTwinCLI_ErrorsNameTheCause(t *testing.T) {
	p := newTwinProject(t, twinSchema)
	ctx := twinCtx()
	id := p.createScenario(t, "Checkout")
	link := func(entityID, system, externalID string) error {
		return (&TwinLinkCmd{EntityID: entityID, System: system, ExternalID: externalID,
			URL: "https://example.com"}).Run(ctx, p.w)
	}
	_ = withOutput(t, output.FormatTable)
	require.NoError(t, link(id, "basecamp", "todo-1"))
	other := p.createScenario(t, "Refund")
	cancelled := p.createScenario(t, "Cancelled")
	require.NoError(t, link(cancelled, "basecamp", "todo-g"))
	require.NoError(t, (&TwinGoneCmd{System: "basecamp", ExternalID: "todo-g"}).Run(ctx, p.w))
	cancelledVersion := p.version(t, cancelled)
	remote := p.remoteFile(t, `{"properties": {"status": "done"}}`)

	tests := []struct {
		name    string
		run     func() error
		is      error
		message string
	}{
		{"no pact with system", func() error { return link(other, "github", "12") },
			twins.ErrNoPact, `entity type scenario has no pact with "github"`},
		{"external id taken", func() error { return link(other, "basecamp", "todo-1") },
			twins.ErrExternalIDTaken, "basecamp/todo-1 is already linked; unlink it first"},
		{"duplicate target", func() error { return link(id, "basecamp", "todo-2") },
			twins.ErrDuplicateTarget, "already has a twin in basecamp"},
		{"invalid external id", func() error { return link(other, "basecamp", "owner/repo#12") },
			twins.ErrInvalidExternalID, `invalid external id "owner/repo#12"`},
		{"missing entity", func() error { return link("SC-999", "basecamp", "todo-9") },
			store.ErrNotFound, "entity SC-999 not found"},
		{"unknown twin", func() error {
			return (&TwinGoneCmd{System: "basecamp", ExternalID: "nope"}).Run(ctx, p.w)
		}, twins.ErrNotFound, "no twin basecamp/nope"},
		{"pact of unknown system", func() error {
			return (&TwinPactCmd{EntityType: "scenario", System: "github"}).Run(ctx, p.w)
		}, twins.ErrNoPact, `entity type "scenario" has no pact with "github"`},
		{"pull of a gone twin", func() error {
			return (&TwinPullCmd{System: "basecamp", ExternalID: "todo-g", Remote: remote,
				RemoteUpdatedAt: "2026-09-20T14:03:11Z"}).Run(ctx, p.w)
		}, twins.ErrGone, "basecamp/todo-g is gone; unlink it first"},
		{"pushed of a gone twin", func() error {
			return (&TwinPushedCmd{System: "basecamp", ExternalID: "todo-g", Version: cancelledVersion}).Run(ctx, p.w)
		}, twins.ErrGone, "basecamp/todo-g is gone; unlink it first"},
		{"twin changed while the command ran", func() error {
			return twinError(fmt.Errorf("twins: %w: basecamp/todo-1 now targets SC-9, not SC-1", twins.ErrStale),
				"basecamp", "todo-1")
		}, twins.ErrStale, "basecamp/todo-1 changed while this command ran"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.ErrorIs(t, err, tc.is)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}

// TestTwinCLI_ShowUsesCurrentType: the type a twin records at link time goes
// stale when the entity's type is renamed. show must still report what the
// write guard enforces, which resolves the pact from the entity's current
// type.
func TestTwinCLI_ShowUsesCurrentType(t *testing.T) {
	p := newTwinProject(t, twinSchema)
	ctx := twinCtx()
	id := p.createScenario(t, "Checkout")
	_ = withOutput(t, output.FormatTable)
	require.NoError(t, (&TwinLinkCmd{EntityID: id, System: "basecamp", ExternalID: "todo-1",
		URL: "https://example.com/todos/1"}).Run(ctx, p.w))

	// Stand in for a type rename, which leaves the recorded type behind.
	st, err := filetwins.New(storage.NewOsFS(), filepath.Join(p.svc.Paths().CacheDir, "twins"))
	require.NoError(t, err)
	_, err = st.Modify(ctx, "basecamp", "todo-1", func(tw twins.Twin) (twins.Twin, error) {
		tw.Target.Type = "story"
		return tw, nil
	})
	require.NoError(t, err)

	var theirs *entitymanager.TheirsWriteError
	require.ErrorAs(t, (&UpdateCmd{ID: id, Status: "done"}).Run(ctx, p.w), &theirs, "the guard still refuses")
	var shown []map[string]any
	p.runJSON(t, &TwinShowCmd{EntityID: id}, &shown)
	require.Len(t, shown, 1)
	assert.Equal(t, []any{"body", "code", "status"}, shown[0]["owned_fields"])
}

// TestTwinCLI_NoPacts: without a pact in the schema every verb says so.
func TestTwinCLI_NoPacts(t *testing.T) {
	p := newTwinProject(t, scenarioTypeYAML+"relations: {}\n")
	require.Nil(t, p.w.Twins)
	_ = withOutput(t, output.FormatTable)
	remote := p.remoteFile(t, `{}`)
	for _, cmd := range []twinRunner{
		&TwinLinkCmd{EntityID: "SC-1", System: "basecamp", ExternalID: "1", URL: "https://example.com"},
		&TwinUnlinkCmd{System: "basecamp", ExternalID: "1"},
		&TwinShowCmd{EntityID: "SC-1"},
		&TwinListCmd{},
		&TwinPendingCmd{},
		&TwinPullCmd{System: "basecamp", ExternalID: "1", Remote: remote, RemoteUpdatedAt: "2026-09-20T14:03:11Z"},
		&TwinPushedCmd{System: "basecamp", ExternalID: "1", Version: "v"},
		&TwinGoneCmd{System: "basecamp", ExternalID: "1"},
		&TwinPactCmd{EntityType: "scenario", System: "basecamp"},
	} {
		err := cmd.Run(twinCtx(), p.w)
		require.ErrorIs(t, err, errNoPacts, "%T", cmd)
		assert.Contains(t, err.Error(), "no pacts declared in schema.yaml")
	}
}

// TestTwinCLI_Pact prints the brief with every ownership list.
func TestTwinCLI_Pact(t *testing.T) {
	p := newTwinProject(t, twinSchema)
	var pact map[string]any
	p.runJSON(t, &TwinPactCmd{EntityType: "scenario", System: "basecamp"}, &pact)
	assert.Equal(t, map[string]any{
		"entity_type":  "scenario",
		"system":       "basecamp",
		"scope":        "https://example.com/lists/1",
		"theirs":       []any{"body", "code", "status"},
		"shared":       []any{},
		"ours":         []any{"title"},
		"propose":      []any{},
		"instructions": "Map todo.completed to status.",
	}, pact)
}

// TestDecodeRemote pins the remote document's absent-versus-null contract.
func TestDecodeRemote(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name    string
		doc     string
		want    twins.Remote
		wantErr string
	}{
		{name: "absent property and body are not mapped", doc: `{"properties": {"status": "done"}}`,
			want: twins.Remote{Properties: map[string]any{"status": "done"}}},
		{name: "null property is cleared", doc: `{"properties": {"due": null}}`,
			want: twins.Remote{Properties: map[string]any{"due": nil}}},
		{name: "null body is cleared", doc: `{"body": null}`, want: twins.Remote{Body: str("")}},
		{name: "string body", doc: `{"body": "text"}`, want: twins.Remote{Body: str("text")}},
		{name: "body inside properties is refused", doc: `{"properties": {"body": "x"}}`,
			wantErr: `"body" is not a property`},
		{name: "unknown key is refused", doc: `{"propertes": {"status": "done"}}`, wantErr: "unknown field"},
		{name: "non-string body is refused", doc: `{"body": 3}`, wantErr: `"body" must be a string or null`},
		{name: "trailing data is refused", doc: `{} {}`, wantErr: "unexpected data"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeRemote([]byte(tc.doc))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// readAudit concatenates every audit file of the project.
func readAudit(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(filepath.Join(root, ".rela", "audit"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		sb.Write(b)
		return err
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading audit: %v", err)
	}
	return sb.String()
}
