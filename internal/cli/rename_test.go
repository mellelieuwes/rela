package cli

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/appbuild/appbuildtest"
	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/output"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/renametype"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/store/memstore"
	"github.com/Sourcehaven-BV/rela/internal/testutil"
)

// renameTestEnv is the bundle returned by setupRenameTestEnv; the
// rename tests need the temp dir and the project paths in order to
// inspect file-on-disk side effects.
type renameTestEnv struct {
	dir   string
	paths *project.Context
	svc   *writeServices
	rt    *renametype.Service
}

func setupRenameTestEnv(t *testing.T) renameTestEnv {
	t.Helper()
	dir := t.TempDir()

	_ = withOutput(t, output.FormatTable)
	paths := &project.Context{
		Root:                 dir,
		EntitiesDir:          filepath.Join(dir, "entities"),
		RelationsDir:         filepath.Join(dir, "relations"),
		CacheDir:             filepath.Join(dir, ".rela"),
		SchemaPath:           filepath.Join(dir, "metamodel.yaml"),
		TemplatesDir:         filepath.Join(dir, "templates"),
		EntityTemplatesDir:   filepath.Join(dir, "templates", "entities"),
		RelationTemplatesDir: filepath.Join(dir, "templates", "relations"),
	}

	// Create directories
	os.MkdirAll(filepath.Join(dir, ".rela"), 0755)
	os.MkdirAll(filepath.Join(dir, "entities", "requirements"), 0755)
	os.MkdirAll(filepath.Join(dir, "relations"), 0755)

	// Write metamodel using shared helper
	metamodelYAML := testutil.SimpleMetamodelYAML()
	os.WriteFile(paths.SchemaPath, []byte(metamodelYAML), 0644)

	// Load metamodel
	meta, err := metamodel.Parse([]byte(metamodelYAML))
	if err != nil {
		t.Fatalf("failed to parse metamodel: %v", err)
	}

	// Set up project services backed by a real filesystem so the
	// rename command can modify files on disk.
	fs := storage.NewSafeFS(storage.NewOsFS())
	b, err := newCLIBundles(
		appbuildtest.New(meta, appbuildtest.WithFS(fs, paths)),
	)
	if err != nil {
		t.Fatalf("newCLIBundles: %v", err)
	}

	return renameTestEnv{dir: dir, paths: paths, svc: b.write, rt: b.renametype}
}

func writeEntityFile(t *testing.T, path, id, entityType, title string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0755)
	content := "---\nid: " + id + "\ntype: " + entityType + "\ntitle: " + title + "\n---\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write entity file: %v", err)
	}
}

func TestValidateTypeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid lowercase", "ticket", false},
		{"valid with hyphen", "risk-item", false},
		{"valid with underscore", "risk_item", false},
		{"valid with digits", "issue2", false},
		{"empty", "", true},
		{"uppercase", "Ticket", true},
		{"starts with digit", "1ticket", true},
		{"path traversal dots", "a..b", true},
		{"path traversal slash", "a/b", true},
		{"path traversal backslash", "a\\b", true},
		{"spaces", "risk item", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTypeName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("validateTypeName(%q) expected error, got nil", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateTypeName(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestRenameEntityCommand(t *testing.T) {
	t.Run("renames entity type end to end", func(t *testing.T) {
		env := setupRenameTestEnv(t)
		dir := env.dir

		// Create entity files
		writeEntityFile(t,
			filepath.Join(dir, "entities", "requirements", "REQ-001.md"),
			"REQ-001", "requirement", "First Requirement")
		writeEntityFile(t,
			filepath.Join(dir, "entities", "requirements", "REQ-002.md"),
			"REQ-002", "requirement", "Second Requirement")

		// Run rename
		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "feature", true, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Check metamodel was updated
		mmData, err := os.ReadFile(env.paths.SchemaPath)
		if err != nil {
			t.Fatalf("failed to read metamodel: %v", err)
		}
		mmContent := string(mmData)

		if strings.Contains(mmContent, "requirement:") {
			t.Error("metamodel should not contain 'requirement:' key")
		}
		if !strings.Contains(mmContent, "feature:") {
			t.Error("metamodel should contain 'feature:' key")
		}
		if !strings.Contains(mmContent, "to: [feature]") && !strings.Contains(mmContent, "- feature") {
			t.Error("metamodel relation 'to' should reference 'feature'")
		}

		// Check directory was renamed
		if _, statErr := os.Stat(filepath.Join(dir, "entities", "requirements")); !os.IsNotExist(statErr) {
			t.Error("old directory should not exist")
		}
		if _, statErr := os.Stat(filepath.Join(dir, "entities", "features")); statErr != nil {
			t.Error("new directory should exist")
		}

		// Check entity files were updated
		data, err := os.ReadFile(filepath.Join(dir, "entities", "features", "REQ-001.md"))
		if err != nil {
			t.Fatalf("failed to read renamed entity: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "type: feature") {
			t.Errorf("entity file should have type 'feature', got:\n%s", content)
		}
		if !strings.Contains(content, "id: REQ-001") {
			t.Errorf("entity file should keep original ID, got:\n%s", content)
		}
	})

	t.Run("error when old type not found", func(t *testing.T) {
		env := setupRenameTestEnv(t)

		err := runRenameEntity(t.Context(), env.svc, env.rt, "nonexistent", "feature", true, "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' error, got: %v", err)
		}
	})

	t.Run("error when new type already exists", func(t *testing.T) {
		env := setupRenameTestEnv(t)

		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "decision", true, "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected 'already exists' error, got: %v", err)
		}
	})

	t.Run("error when new type name is invalid", func(t *testing.T) {
		env := setupRenameTestEnv(t)

		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "Bad-Name", true, "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid type name") {
			t.Errorf("expected 'invalid type name' error, got: %v", err)
		}
	})

	t.Run("custom plural form", func(t *testing.T) {
		env := setupRenameTestEnv(t)
		dir := env.dir

		writeEntityFile(t,
			filepath.Join(dir, "entities", "requirements", "REQ-001.md"),
			"REQ-001", "requirement", "Test")

		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "policy", true, "policies")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Check custom plural was used for directory
		if _, err := os.Stat(filepath.Join(dir, "entities", "policies")); err != nil {
			t.Error("directory should use custom plural 'policies'")
		}
	})

	t.Run("renames template if exists", func(t *testing.T) {
		env := setupRenameTestEnv(t)
		dir := env.dir

		// Create template
		templateDir := filepath.Join(dir, "templates", "entities")
		os.MkdirAll(templateDir, 0755)
		templatePath := filepath.Join(templateDir, "requirement.md")
		os.WriteFile(templatePath, []byte("---\nstatus: draft\n---\n"), 0644)

		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "feature", true, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Check template was renamed
		if _, err := os.Stat(filepath.Join(templateDir, "requirement.md")); !os.IsNotExist(err) {
			t.Error("old template should not exist")
		}
		if _, err := os.Stat(filepath.Join(templateDir, "feature.md")); err != nil {
			t.Error("new template should exist")
		}
	})

	t.Run("handles no entity directory", func(t *testing.T) {
		env := setupRenameTestEnv(t)

		// Remove the entities directory entirely
		os.RemoveAll(env.paths.EntitiesDir)
		os.MkdirAll(env.paths.EntitiesDir, 0755)

		err := runRenameEntity(t.Context(), env.svc, env.rt, "requirement", "feature", true, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Metamodel should still be updated
		mmData, _ := os.ReadFile(env.paths.SchemaPath)
		if !strings.Contains(string(mmData), "feature:") {
			t.Error("metamodel should contain 'feature:' key even with no entity directory")
		}
	})
}

// TestRenameIDCommand_UnderACLPolicy pins BUG: `rela rename id` must run
// under the principal the CLI stamps on its context. With an acl.yaml every
// write is authorized against that principal, so a command that dropped it for
// context.Background() failed with "principal is unstamped" even for a user
// the policy grants — and without a policy it recorded the rename as nobody's.
func TestRenameIDCommand_UnderACLPolicy(t *testing.T) {
	_ = withOutput(t, output.FormatTable)
	meta, err := metamodel.Parse([]byte(testutil.SimpleMetamodelYAML()))
	if err != nil {
		t.Fatalf("parse metamodel: %v", err)
	}

	policyPath := filepath.Join(t.TempDir(), "acl.yaml")
	policyYAML := "roles:\n  admin:\n    create: [\"*\"]\n    update: [\"*\"]\n    delete: [\"*\"]\n    read: [\"*\"]\n" +
		"assignments:\n  alice: admin\n"
	if err = os.WriteFile(policyPath, []byte(policyYAML), 0o600); err != nil {
		t.Fatalf("write acl.yaml: %v", err)
	}
	policy, err := acl.LoadPolicy(policyPath)
	if err != nil {
		t.Fatalf("load acl.yaml: %v", err)
	}

	st := memstore.New()
	decl, err := acl.NewDeclarative(policy, acl.NewStoreGraph(st), st,
		acl.WithPrincipalLookup(acl.NewStorePrincipalLookup(st)))
	if err != nil {
		t.Fatalf("acl.NewDeclarative: %v", err)
	}
	sink := audit.NewMemory()
	b, err := newCLIBundles(appbuildtest.New(meta,
		appbuildtest.WithStore(st), appbuildtest.WithDeclarative(decl), appbuildtest.WithAudit(sink)))
	if err != nil {
		t.Fatalf("newCLIBundles: %v", err)
	}
	if err = st.CreateEntity(t.Context(),
		testutil.EntityFor(meta, "requirement").ID("REQ-001").Build()); err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	ctx := principal.With(context.Background(), principal.Principal{User: "alice", Tool: principal.ToolCLI})
	if err = (&RenameIDCmd{OldID: "REQ-001", NewID: "REQ-100"}).Run(ctx, b.write); err != nil {
		t.Fatalf("rename id by a user the policy grants: %v", err)
	}

	if _, err = st.GetEntity(t.Context(), "REQ-100"); err != nil {
		t.Fatalf("renamed entity not found under its new id: %v", err)
	}
	if _, err = st.GetEntity(t.Context(), "REQ-001"); err == nil || !stderrors.Is(err, store.ErrNotFound) {
		t.Fatalf("old id must be gone after the rename, got err=%v", err)
	}
	var renameRec *audit.Record
	for _, rec := range sink.Records() {
		if rec.Op == audit.OpRenameEntity {
			renameRec = &rec
		}
	}
	if renameRec == nil {
		t.Fatalf("no rename-entity audit record; got %+v", sink.Records())
	}
	if renameRec.Principal.User != "alice" || renameRec.Principal.Tool != principal.ToolCLI {
		t.Errorf("rename attributed to %+v, want alice via %s", renameRec.Principal, principal.ToolCLI)
	}
}
