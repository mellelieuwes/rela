package cli

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/storage"
)

// AM-acl-audit-permission-consumers-complete, part 1.
//
// Each data-entry surface that carries a `permission:` is tested on its own,
// so dropping one from dataEntryPermissions.UsedPermissions fails a test
// rather than silently making the audit report a working grant as dead
// config. A single combined fixture would pass with three of four surfaces
// missing — the same incompleteness that caused BUG-919PM6.
func TestDataEntryPermissions_PerSurface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "standalone document",
			yaml: `
documents:
  sales-report:
    title: Sales report
    permission: report:sales
`,
		},
		{
			name: "dashboard card",
			yaml: `
dashboard:
  cards:
    - title: Sales
      permission: report:sales
`,
		},
		{
			name: "navigation entry",
			yaml: `
navigation:
  - label: Sales
    list: orders
    permission: report:sales
`,
		},
		{
			name: "nested navigation entry",
			yaml: `
navigation:
  - group: Reports
    items:
      - label: Sales
        list: orders
        permission: report:sales
`,
		},
		{
			name: "command",
			yaml: `
commands:
  export-sales:
    label: Export
    permission: report:sales
`,
		},
		{
			name: "action",
			yaml: `
actions:
  regenerate-sales:
    script: regenerate.lua
    permission: report:sales
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			perms, err := loadDataEntryPermissions(loaderWith(t, tc.yaml), nil)
			if err != nil {
				t.Fatalf("loadDataEntryPermissions: %v", err)
			}
			if got := perms.UsedPermissions(); !slices.Contains(got, "report:sales") {
				t.Errorf("permission on a %s not collected; got %q", tc.name, got)
			}
		})
	}
}

// Surfaces without a permission contribute nothing — the adapter must not emit
// empty strings for every unpermissioned entry.
func TestDataEntryPermissions_SkipsUnpermissionedSurfaces(t *testing.T) {
	t.Parallel()
	perms, err := loadDataEntryPermissions(loaderWith(t, `
navigation:
  - label: Open
    list: orders
  - group: Reports
    items:
      - label: Sales
        list: orders
        permission: report:sales
documents:
  public-report:
    title: Public
`), nil)
	if err != nil {
		t.Fatalf("loadDataEntryPermissions: %v", err)
	}
	want := []string{"report:sales"}
	if got := perms.UsedPermissions(); !slices.Equal(got, want) {
		t.Errorf("UsedPermissions() = %q, want %q (no empty strings)", got, want)
	}
}

// A missing data-entry.yaml is COMPLETE information — the project has no UI
// gates — so it yields a usable consumer, not nil. Returning nil here would
// suppress A7 for every project that has no data-entry.yaml.
func TestPermissionConsumerFor_MissingFileIsNotNil(t *testing.T) {
	t.Parallel()
	perms, err := permissionConsumerFor(emptyLoader(t), nil)
	if err != nil {
		t.Fatalf("missing data-entry.yaml must not error: %v", err)
	}
	if perms == nil {
		t.Fatal("missing data-entry.yaml must yield a consumer, not nil (nil suppresses A7)")
	}
	if got := perms.UsedPermissions(); len(got) != 0 {
		t.Errorf("expected no permissions, got %q", got)
	}
}

// RR-8OZ0HS. The nil case must be a nil INTERFACE, not a non-nil interface
// wrapping a nil *dataEntryPermissions — aclaudit.Audit tests `perms == nil`,
// and a typed nil would sail past it and let A7 run blind on config it could
// not read. That is the regression BUG-919PM6 exists to prevent, and it lives
// at this call site rather than inside aclaudit.
func TestPermissionConsumerFor_UnreadableConfigYieldsUntypedNil(t *testing.T) {
	t.Parallel()
	perms, err := permissionConsumerFor(loaderWith(t, "navigation: [unclosed\n"), nil)
	if err == nil {
		t.Fatal("malformed data-entry.yaml must error so the caller can suppress A7")
	}
	if perms != nil {
		t.Fatalf("expected a nil consumer, got %#v", perms)
	}
	// `perms != nil` above passes for an untyped nil AND fails for a typed one,
	// but only because the helper returns the interface type. Assert the
	// stronger property directly, so a refactor that returns the concrete type
	// and relies on the caller to convert cannot slip through.
	if v := reflect.ValueOf(perms); v.IsValid() {
		t.Errorf("consumer holds a concrete value (%s) rather than being an untyped nil; "+
			"aclaudit.Audit's perms == nil check would not fire", v.Type())
	}
}

// The adapter must not validate: the audit's subject is acl.yaml, and a
// project whose data-entry.yaml is invalid should still get its ACL findings.
func TestDataEntryPermissions_DoesNotValidateConfig(t *testing.T) {
	t.Parallel()
	perms, err := loadDataEntryPermissions(loaderWith(t, `
navigation:
  - label: Sales
    list: no-such-list-in-any-metamodel
    permission: report:sales
`), nil)
	if err != nil {
		t.Fatalf("adapter must not validate against the metamodel: %v", err)
	}
	if got := perms.UsedPermissions(); !slices.Contains(got, "report:sales") {
		t.Errorf("expected report:sales despite an invalid list ref, got %q", got)
	}
}

// loaderWith returns a config.Loader serving body as data-entry.yaml. It goes
// through storage.MemFS rather than t.TempDir() so the tests exercise the same
// injected-loader path production uses, without touching disk.
func loaderWith(t *testing.T, body string) config.Loader {
	t.Helper()
	const root = "/project"
	fs := storage.NewMemFS()
	if err := fs.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	path := filepath.Join(root, dataentryconfig.ConfigFile)
	if err := fs.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return config.NewFSLoader(fs, root)
}

// emptyLoader returns a config.Loader over a filesystem with no config file.
func emptyLoader(t *testing.T) config.Loader {
	t.Helper()
	const root = "/project"
	fs := storage.NewMemFS()
	if err := fs.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	return config.NewFSLoader(fs, root)
}

// A copy's `guard.permission` lives in schema.yaml, not data-entry.yaml, so
// collecting only the data-entry surfaces reported every copy guard as dead
// config — advising the operator to delete the permission that gates
// publishing (observed on the shipped worlds prototype, QA F-D).
func TestDataEntryPermissions_CollectsCopyGuardPermissions(t *testing.T) {
	t.Parallel()
	meta := &metamodel.Metamodel{Copies: map[string]metamodel.CopyDef{
		"publish": {Guard: metamodel.CopyGuard{Permission: "publish-policy"}},
		"ungated": {},
	}}
	perms := (&dataEntryPermissions{cfg: &dataentryconfig.Config{}, meta: meta}).UsedPermissions()
	if !slices.Contains(perms, "publish-policy") {
		t.Errorf("a copy guard permission must count as used, else A7 reports it dead; got %q", perms)
	}
}

// A nil metamodel must not panic and must not lose the data-entry half: a
// project whose schema could not be read still deserves the surfaces that were
// readable.
func TestDataEntryPermissions_NilMetamodelKeepsDataEntryPermissions(t *testing.T) {
	t.Parallel()
	cfg := &dataentryconfig.Config{Commands: map[string]dataentryconfig.CommandConfig{
		"report": {Permission: "run-report"},
	}}
	perms := (&dataEntryPermissions{cfg: cfg, meta: nil}).UsedPermissions()
	if !slices.Contains(perms, "run-report") {
		t.Errorf("a nil metamodel must not suppress the data-entry surfaces; got %q", perms)
	}
}
