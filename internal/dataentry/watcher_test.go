package dataentry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/migration"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

// Minimal metamodel YAML for reload tests.
const testReloadMetamodelYAML = `version: "1.0"
entities:
  ticket:
    label: Ticket
    plural: tickets
    id_prefix: "TKT-"
    id_type: sequential
    properties:
      title:
        type: string
        required: true
      status:
        type: string
      due:
        type: date
relations:
  depends_on:
    label: depends on
    from: [ticket]
    to: [ticket]
`

// Minimal data-entry config YAML for reload tests.
const testReloadConfigYAML = `version: "1.0"
app:
  name: "Test App"
forms:
  create_ticket:
    entity_type: ticket
    title: "New Ticket"
    fields:
      - property: title
        label: "Title"
lists:
  tickets:
    entity_type: ticket
    title: "Tickets"
    columns:
      - property: title
        label: "Title"
navigation:
  - label: "Tickets"
    list: tickets
`

// setupReloadTestApp creates an App backed by MemFS for testing reload and broker logic.
func setupReloadTestApp(t *testing.T) (*App, *storage.MemFS) {
	t.Helper()
	return setupReloadTestAppAt(t, "/project")
}

// setupReloadTestAppAt is setupReloadTestApp with the project rooted at root.
// Script existence checks read the real disk through os.OpenRoot, so tests
// that exercise them pass a t.TempDir() and put scripts there.
func setupReloadTestAppAt(t *testing.T, root string) (*App, *storage.MemFS) {
	t.Helper()

	fs := storage.NewMemFS()

	ctx := &project.Context{
		Root:                 root,
		SchemaPath:           root + "/metamodel.yaml",
		CacheDir:             root + "/.rela",
		EntitiesDir:          root + "/entities",
		RelationsDir:         root + "/relations",
		TemplatesDir:         root + "/templates",
		EntityTemplatesDir:   root + "/templates/entities",
		RelationTemplatesDir: root + "/templates/relations",
	}

	// Create directory structure
	_ = fs.MkdirAll(ctx.EntitiesDir+"/tickets", 0o755)
	_ = fs.MkdirAll(ctx.RelationsDir, 0o755)
	_ = fs.MkdirAll(ctx.CacheDir, 0o755)
	_ = fs.MkdirAll(ctx.EntityTemplatesDir, 0o755)
	_ = fs.MkdirAll(ctx.RelationTemplatesDir, 0o755)

	// Write metamodel and config files
	_ = fs.WriteFile(ctx.SchemaPath, []byte(testReloadMetamodelYAML), 0o644)
	_ = fs.WriteFile(root+"/data-entry.yaml", []byte(testReloadConfigYAML), 0o644)

	// Write a sample entity
	_ = fs.WriteFile(ctx.EntitiesDir+"/tickets/TKT-001.md", []byte(`---
id: TKT-001
type: ticket
title: First Ticket
status: open
---
`), 0o644)

	meta, _, err := metamodel.NewFSLoader(fs, ctx.SchemaPath).Load(context.Background())
	if err != nil {
		t.Fatalf("failed to load metamodel: %v", err)
	}

	g := newFixture()

	cfg := &Config{
		App: AppConfig{Name: "Test App"},
	}

	svc := appbuildtest.New(meta, appbuildtest.WithFS(fs, ctx))
	seedFromFixture(svc.Store(), g)

	app := newAppFromParts(cfg, meta, g)
	rebindApp(app, fs, ctx, svc)
	app.broker = newEventBroker()

	return app, fs
}

// --- eventBroker tests ---

func TestEventBrokerSubscribeUnsubscribe(t *testing.T) {
	b := newEventBroker()

	ch1 := b.subscribe()
	ch2 := b.subscribe()

	b.mu.Lock()
	count := len(b.clients)
	b.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 subscribers, got %d", count)
	}

	b.unsubscribe(ch1)

	b.mu.Lock()
	count = len(b.clients)
	b.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 subscriber after unsubscribe, got %d", count)
	}

	b.unsubscribe(ch2)

	b.mu.Lock()
	count = len(b.clients)
	b.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 subscribers, got %d", count)
	}
}

func TestEventBrokerBroadcast(t *testing.T) {
	b := newEventBroker()
	ch1 := b.subscribe()
	ch2 := b.subscribe()

	b.broadcast("refresh")

	select {
	case msg := <-ch1:
		if msg.Name != "refresh" {
			t.Errorf("ch1: expected 'refresh', got %q", msg.Name)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("ch1: timed out waiting for broadcast")
	}

	select {
	case msg := <-ch2:
		if msg.Name != "refresh" {
			t.Errorf("ch2: expected 'refresh', got %q", msg.Name)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("ch2: timed out waiting for broadcast")
	}
}

func TestEventBrokerBroadcastSkipsSlowClient(t *testing.T) {
	b := newEventBroker()
	ch := b.subscribe()

	// Fill the channel buffer (capacity 4)
	b.broadcast("first")
	b.broadcast("second")
	b.broadcast("third")
	b.broadcast("fourth")
	// Fifth broadcast should not block — slow client is skipped
	b.broadcast("fifth")

	// Drain all 4 buffered messages
	for range 4 {
		<-ch
	}

	// Channel should be empty now (fifth was dropped)
	select {
	case extra := <-ch:
		t.Errorf("expected no more messages, got %q", extra.Name)
	default:
		// expected
	}
}

func TestEventBrokerUnsubscribeIdempotent(_ *testing.T) {
	b := newEventBroker()
	ch := b.subscribe()

	b.unsubscribe(ch)
	// Double unsubscribe should not panic
	b.unsubscribe(ch)
}

func TestEventBrokerConcurrency(t *testing.T) {
	b := newEventBroker()
	var wg sync.WaitGroup

	// Concurrently subscribe, broadcast, and unsubscribe
	for range 20 {
		wg.Go(func() {
			ch := b.subscribe()
			b.broadcast("test")
			// drain
			select {
			case <-ch:
			default:
			}
			b.unsubscribe(ch)
		})
	}
	wg.Wait()

	b.mu.Lock()
	remaining := len(b.clients)
	b.mu.Unlock()
	if remaining != 0 {
		t.Errorf("expected 0 clients after concurrent test, got %d", remaining)
	}
}

// simulateReload mimics what the data-entry.yaml subscriber does in
// production: a config-path event triggers reloadConfig. Other
// event paths are ignored — entity/relation changes are reconciled by
// the store's own observer chain (see [App.StartWatching]), not via a
// dataentry-level callback.
func (a *App) simulateReload(events []storage.ChangeEvent) {
	configPath := a.paths.Root + "/" + ConfigFile
	for _, e := range events {
		if e.Path == configPath {
			_ = a.reloadConfig() // callers assert on the published config
			return
		}
	}
}

// --- reload tests ---

func TestReloadConfigChange(t *testing.T) {
	app, fs := setupReloadTestApp(t)

	originalName := app.Cfg().App.Name

	// Write updated config with a different app name
	updatedConfig := `version: "1.0"
app:
  name: "Updated App"
lists: {}
forms: {}
navigation: []
`
	configPath := app.paths.Root + "/" + ConfigFile
	_ = fs.WriteFile(configPath, []byte(updatedConfig), 0o644)

	app.simulateReload([]storage.ChangeEvent{
		{Path: configPath, Op: storage.OpModify},
	})

	if app.Cfg().App.Name == originalName {
		t.Error("expected config app name to change after reload")
	}
	if app.Cfg().App.Name != "Updated App" {
		t.Errorf("expected 'Updated App', got %q", app.Cfg().App.Name)
	}
}

// TestReloadRejectedConfigKeepsPrevious pins TKT-IMBOK: a reload runs the
// same checks as startup, and a config startup would refuse leaves the
// previous one serving instead of being published.
func TestReloadRejectedConfigKeepsPrevious(t *testing.T) {
	const header = "version: \"1.0\"\napp:\n  name: \"Rejected App\"\n"
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "unparsable yaml",
			config:  "not: valid: yaml: {{{",
			wantErr: "parsing data-entry.yaml",
		},
		{
			name: "document with both command and script",
			config: header + `documents:
  report:
    title: Report
    command: ["echo", "hi"]
    script: report.lua
`,
			wantErr: "invalid data-entry.yaml",
		},
		{
			name: "missing action script",
			config: header + `actions:
  close:
    label: Close
    entity_types: [ticket]
    script: missing.lua
`,
			wantErr: `action "close"`,
		},
		{
			name: "missing document script",
			config: header + `documents:
  report:
    title: Report
    script: missing.lua
`,
			wantErr: `document "report"`,
		},
		{
			name: "missing export_render script",
			config: header + `lists:
  tickets:
    entity_type: ticket
    title: Tickets
    export_render: missing.lua
    columns:
      - property: title
        label: Title
`,
			wantErr: `list "tickets": export_render`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"actions", "scripts"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			app, fs := setupReloadTestAppAt(t, root)
			before := app.Cfg()
			_ = fs.WriteFile(filepath.Join(root, ConfigFile), []byte(tc.config), 0o644)

			err := app.reloadConfig()

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("reloadConfig error = %v, want one containing %q", err, tc.wantErr)
			}
			if app.Cfg() != before {
				t.Errorf("rejected reload replaced the config: app name now %q", app.Cfg().App.Name)
			}
		})
	}
}

// TestReloadAcceptsExistingScripts is the positive counterpart: the same
// shapes with their scripts present reload cleanly.
func TestReloadAcceptsExistingScripts(t *testing.T) {
	root := t.TempDir()
	for dir, file := range map[string]string{"actions": "close.lua", "scripts": "report.lua"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, file), []byte("return nil\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app, fs := setupReloadTestAppAt(t, root)
	_ = fs.WriteFile(filepath.Join(root, ConfigFile), []byte(`version: "1.0"
app:
  name: "Scripted App"
actions:
  close:
    label: Close
    entity_types: [ticket]
    script: close.lua
documents:
  report:
    title: Report
    script: report.lua
`), 0o644)

	if err := app.reloadConfig(); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if got := app.Cfg().App.Name; got != "Scripted App" {
		t.Errorf("app name = %q, want %q", got, "Scripted App")
	}
}

// TestReloadNormalizesCalendars pins that a reloaded config gets the same
// defaults startup fills in; the old reload path skipped normalization.
func TestReloadNormalizesCalendars(t *testing.T) {
	app, fs := setupReloadTestApp(t)
	_ = fs.WriteFile(app.paths.Root+"/"+ConfigFile, []byte(`version: "1.0"
app:
  name: "Calendar App"
calendars:
  schedule:
    title: Schedule
    sources:
      - entity_type: ticket
        date: due
`), 0o644)

	if err := app.reloadConfig(); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if got := app.Cfg().Calendars["schedule"].DefaultView; got == "" {
		t.Error("reloaded calendar has no default_view; normalization did not run")
	}
}

// TestReloadConfigBroadcasts pins the SSE contract of reloadConfig: refresh after an accepted reload, config-error (and no refresh)
// after a rejected one.
func TestReloadConfigBroadcasts(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		remove    bool
		wantEvent string
		wantData  string
	}{
		{
			name:      "accepted",
			config:    testReloadConfigYAML,
			wantEvent: "refresh",
			wantData:  "refresh",
		},
		{
			name:      "rejected",
			config:    "not: valid: yaml: {{{",
			wantEvent: "config-error",
			wantData:  `"error":"parsing data-entry.yaml`,
		},
		{
			name:      "unreadable",
			remove:    true,
			wantEvent: "config-error",
			wantData:  `"error":"` + configReadFailedMessage + `"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, fs := setupReloadTestApp(t)
			path := app.paths.Root + "/" + ConfigFile
			if tc.remove {
				_ = fs.Remove(path)
			} else {
				_ = fs.WriteFile(path, []byte(tc.config), 0o644)
			}
			ch := app.broker.subscribe()
			defer app.broker.unsubscribe(ch)

			_ = app.reloadConfig() // the broadcast is what this test checks

			select {
			case ev := <-ch:
				if ev.Name != tc.wantEvent {
					t.Fatalf("event = %q, want %q", ev.Name, tc.wantEvent)
				}
				if !strings.Contains(ev.Data, tc.wantData) {
					t.Errorf("event data = %s, want it to contain %s", ev.Data, tc.wantData)
				}
			default:
				t.Fatal("no event broadcast")
			}
			select {
			case ev := <-ch:
				t.Errorf("unexpected second event %q", ev.Name)
			default:
			}
		})
	}
}

// TestReloadConfigErrorOmitsHostPath pins that a rejected reload never puts
// the project directory on the SSE wire. The deprecated-syntax error names its
// file by absolute path, so it is the case that would leak without the rewrite.
func TestReloadConfigErrorOmitsHostPath(t *testing.T) {
	root := t.TempDir()
	app, fs := setupReloadTestAppAt(t, root)
	_ = fs.WriteFile(filepath.Join(root, ConfigFile), []byte(`version: "1.0"
app:
  name: "Deprecated"
lists:
  tickets:
    entity_type: ticket
    detail_view: ticket_detail
`), 0o644)
	if len(migration.DetectBytes(mustReadFile(t, fs, filepath.Join(root, ConfigFile)), migration.FileTypeDataEntry)) == 0 {
		t.Fatal("fixture no longer triggers a deprecated-syntax detection; pick another")
	}
	ch := app.broker.subscribe()
	defer app.broker.unsubscribe(ch)

	if err := app.reloadConfig(); err == nil {
		t.Fatal("reloadConfig accepted deprecated syntax")
	}

	var ev sseEvent
	select {
	case ev = <-ch:
	default:
		t.Fatal("no event broadcast")
	}
	if ev.Name != "config-error" {
		t.Fatalf("event = %q, want config-error", ev.Name)
	}
	if strings.Contains(ev.Data, root) {
		t.Errorf("config-error frame carries the project root %q: %s", root, ev.Data)
	}
	if !strings.Contains(ev.Data, "deprecated syntax") {
		t.Errorf("config-error frame lost the reason: %s", ev.Data)
	}
}

func TestPublicConfigErrorMessage_StripsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	err := fmt.Errorf("open %s: permission denied", filepath.Join(root, "actions", "x.lua"))

	got := publicConfigErrorMessage(err, root)

	if strings.Contains(got, root) {
		t.Errorf("message still carries the root: %q", got)
	}
	if want := "open " + filepath.Join("actions", "x.lua") + ": permission denied"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func mustReadFile(t *testing.T, fs *storage.MemFS, path string) []byte {
	t.Helper()
	data, err := fs.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// --- handleSSE tests ---

// flusherRecorder wraps httptest.ResponseRecorder to implement http.Flusher.
// Each Flush is signaled on the flushes channel so tests can wait for the
// SSE handler to reach a known point (subscribed + keepalive written, event
// written) instead of sleeping.
type flusherRecorder struct {
	*httptest.ResponseRecorder
	flushes chan struct{}
}

func newFlusherRecorder() *flusherRecorder {
	return &flusherRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushes:          make(chan struct{}, 16),
	}
}

func (f *flusherRecorder) Flush() {
	f.ResponseRecorder.Flush()
	if f.flushes == nil {
		return
	}
	select {
	case f.flushes <- struct{}{}:
	default: // a full buffer already signals "flushed"; never block the handler
	}
}

// awaitFlush blocks until the handler's next Flush call.
func (f *flusherRecorder) awaitFlush(t *testing.T) {
	t.Helper()
	select {
	case <-f.flushes:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SSE flush")
	}
}

func TestHandleSSEHeaders(t *testing.T) {
	app, _ := setupReloadTestApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/api/events", http.NoBody).WithContext(ctx)
	w := newFlusherRecorder()

	// Run handler in goroutine since it blocks until context is cancelled
	done := make(chan struct{})
	go func() {
		app.handleSSE(w, r)
		close(done)
	}()

	// The first flush means headers and the initial keepalive are written.
	w.awaitFlush(t)
	cancel()
	<-done

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type 'text/event-stream', got %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control 'no-cache', got %q", cc)
	}
	// Regression: SSE must NOT reflect cross-origin requests via CORS headers.
	// The original bug let any website subscribe to live project events.
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("SSE must not set Access-Control-Allow-Origin (security regression), got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("SSE must not set Access-Control-Allow-Credentials (security regression), got %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, ": connected") {
		t.Errorf("expected initial keepalive comment, got %q", body)
	}
}

func TestHandleSSEReceivesEvent(t *testing.T) {
	app, _ := setupReloadTestApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/events", http.NoBody).WithContext(ctx)
	w := newFlusherRecorder()

	done := make(chan struct{})
	go func() {
		app.handleSSE(w, r)
		close(done)
	}()

	// The keepalive flush happens after the handler subscribes, so the
	// broadcast below is guaranteed to reach its channel.
	w.awaitFlush(t)

	// Broadcast an event
	app.broker.broadcast("refresh")

	// Wait for the event to be written
	w.awaitFlush(t)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: refresh") {
		t.Errorf("expected 'event: refresh' in body, got %q", body)
	}
	if !strings.Contains(body, "data: refresh") {
		t.Errorf("expected 'data: refresh' in body, got %q", body)
	}
}

func TestHandleSSENoFlusher(t *testing.T) {
	app, _ := setupReloadTestApp(t)

	r := httptest.NewRequest(http.MethodGet, "/api/events", http.NoBody)
	// Plain ResponseRecorder does not implement http.Flusher — but actually
	// httptest.ResponseRecorder does implement Flusher in Go 1.12+.
	// Use a custom writer that does NOT implement Flusher.
	w := &nonFlusherWriter{header: make(http.Header)}

	app.handleSSE(w, r)

	if w.code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-flusher writer, got %d", w.code)
	}
}

// nonFlusherWriter is an http.ResponseWriter that does NOT implement http.Flusher.
type nonFlusherWriter struct {
	header http.Header
	code   int
	body   strings.Builder
}

func (w *nonFlusherWriter) Header() http.Header         { return w.header }
func (w *nonFlusherWriter) WriteHeader(code int)        { w.code = code }
func (w *nonFlusherWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

// --- noCacheMiddleware tests ---

func TestNoCacheMiddleware(t *testing.T) {
	app, _ := setupReloadTestApp(t)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := app.noCacheMiddleware(inner)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected inner handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestNoCacheMiddlewareSetsHeader(t *testing.T) {
	app, _ := setupReloadTestApp(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := app.noCacheMiddleware(inner)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	cc := w.Header().Get("Cache-Control")
	if cc != "no-cache, no-store, must-revalidate" {
		t.Errorf("expected Cache-Control header, got %q", cc)
	}
}

func TestConcurrentReadDuringOnReload(t *testing.T) {
	// With App.mu deleted, handlers never block on reload — they
	// observe either the pre-reload or post-reload snapshot.
	//
	// This test asserts the snapshot's *cross-field* invariants hold
	// at every observation: a reader that loads State() sees a Cfg,
	// Meta, Graph, StyleMap, etc. that all came from the same publish.
	// A regression that publishes one field independently of another
	// would break the StyleMap entry-count invariant below.
	app, _ := setupReloadTestApp(t)

	const readers = 8
	const duration = 200 * time.Millisecond
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := app.State()
				if s == nil {
					t.Errorf("state.Load() returned nil")
					return
				}
				if s.Cfg == nil || s.Meta == nil {
					t.Errorf("state.Load() returned incomplete snapshot: cfg=%v meta=%v",
						s.Cfg != nil, s.Meta != nil)
					return
				}
				// Cross-field invariant: a published Schema always
				// has a StyleMap that covers every property type in
				// its metamodel. A torn publish would yield zero or
				// fewer entries.
				if len(s.StyleMap) < len(s.Meta.Types) {
					t.Errorf("torn snapshot: %d StyleMap entries vs %d metamodel types",
						len(s.StyleMap), len(s.Meta.Types))
					return
				}
			}
		})
	}

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Hammer the actual reload path (config-driven rebuild)
			// concurrently with reader goroutines so torn snapshots
			// would be observable. A rejected reload publishes nothing,
			// which would make this test vacuous, so fail on one.
			if err := app.reloadConfig(); err != nil {
				t.Errorf("reloadConfig: %v", err)
				return
			}
		}
	})

	time.Sleep(duration)
	close(stop)
	wg.Wait()
}
