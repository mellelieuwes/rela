package dataentry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestProvisionSeam_EveryWriteHandlerUsesWithProvision is the class-level guard
// for the unmatched_principal: provision anti-bypass invariant (TKT-ANUJDS AC6).
//
// Provisioning runs inside withProvision. A write handler that forgets to call
// it silently skips provisioning (and the re-stamp), reintroducing the
// per-handler bypass the reject design review found. Before TKT-WE0S2K the
// guard keyed on the process-wide write lock, which every handler had to take;
// with that lock gone, the invariant is stated directly: every handle* method
// on writeHandler and attachmentHandler, and every route in otherWrites,
// calls withProvision, unless it is listed in notWrites below.
//
// This is a source check rather than a driven test because the action and
// attachment paths need heavy fixture setup to drive; the CRUD path IS driven
// end-to-end in provision_e2e_test.go. Together they pin both that the seam
// works and that no path can skip it.
func TestProvisionSeam_EveryWriteHandlerUsesWithProvision(t *testing.T) {
	files := []string{
		"write_handler.go",
		"actions.go",
		"attachment_handler.go",
		"handlers_attachment.go",
		"comments_wiring.go",
		"comments_handler.go",
	}
	// Handlers that do not write. Adding one here needs a reason.
	notWrites := map[string]string{
		"handleV1DryRunCreate":        "validates only; never persists",
		"handleV1GetAttachment":       "read",
		"handleV1AttachmentRoute":     "dispatcher; the PUT/DELETE handlers it calls provision",
		"handleV1AttachmentFileRoute": "dispatcher; the GET/DELETE handlers it calls provision",
		"handleV1Comments": "dispatcher; comment writes go to the comment store, not the graph, " +
			"and commentAccept, the one entity write it routes to, provisions",
	}
	receivers := map[string]bool{"writeHandler": true, "attachmentHandler": true, "commentsHandler": true}
	// Write routes whose names do not start with "handle".
	otherWrites := map[string]bool{"commentAccept": true}

	fset := token.NewFileSet()
	seen := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil ||
				(!strings.HasPrefix(fn.Name.Name, "handle") && !otherWrites[fn.Name.Name]) {

				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			recv, ok := star.X.(*ast.Ident)
			if !ok || !receivers[recv.Name] {
				continue
			}
			seen++
			if _, skip := notWrites[fn.Name.Name]; skip {
				continue
			}
			if !callsMethod(fn.Body, "withProvision") {
				t.Errorf("%s: %s.%s does not call withProvision; a write handler that skips it "+
					"lets an unmatched principal bypass unmatched_principal: provision",
					fset.Position(fn.Pos()), recv.Name, fn.Name.Name)
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no handler methods; the file list or receiver names are stale")
	}
}

// callsMethod reports whether body contains a call to a method named name.
func callsMethod(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}
