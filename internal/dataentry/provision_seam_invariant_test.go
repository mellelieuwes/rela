package dataentry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProvisionSeam_EveryWriteHandlerUsesEnterWrite is the class-level guard for
// the unmatched_principal: provision anti-bypass invariant (TKT-ANUJDS AC6).
//
// Provisioning runs inside enterWrite, which the write handlers call in place of
// a bare writeMu.Lock(). If a future write handler takes the lock directly, it
// would silently skip provisioning (and the re-stamp), reintroducing exactly the
// per-handler bypass the reject design review found. So the invariant is: in the
// write-handler source files, writeMu.Lock() appears ONLY inside the enterWrite
// methods — every other mutation entry acquires the lock via enterWrite.
//
// This is a source check rather than a driven test because the write handler
// types (writeHandler, attachmentHandler) live in several files and the
// action/attachment paths need heavy fixture setup to drive; the CRUD path IS
// driven end-to-end in provision_e2e_test.go. Together they pin both that the
// seam works and that no path can skip it.
//
// NOTE (TKT-8P1TM7): the sync record write handlers (sync_handlers.go) were
// retired — sync now writes through the v1 CRUD path (write_handler.go), which
// this guard already covers. syncHandler now serves only the manifest (a read),
// so it holds no writeMu and needs no enterWrite; it is dropped from the scan.
func TestProvisionSeam_EveryWriteHandlerUsesEnterWrite(t *testing.T) {
	// The files that hold writeMu-taking mutation handlers.
	files := []string{
		"write_handler.go",
		"actions.go",
		"attachment_handler.go",
		"handlers_attachment.go",
		"comments_wiring.go",
		"comments_handler.go",
	}
	lockRe := regexp.MustCompile(`\bwriteMu\.Lock\(\)`)

	for _, name := range files {
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")
		inEnterWrite := false
		braceDepth := 0
		for i, line := range lines {
			// Track whether we're inside an enterWrite method body (the ONLY
			// sanctioned place a bare writeMu.Lock() may appear).
			if strings.Contains(line, "func (h *") && strings.Contains(line, ") enterWrite(") {
				inEnterWrite = true
				braceDepth = 0
			}
			if inEnterWrite {
				braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
			}
			if lockRe.MatchString(line) && !inEnterWrite {
				t.Errorf("%s:%d takes writeMu.Lock() directly; write handlers MUST acquire "+
					"the lock via enterWrite so unmatched_principal: provision cannot be "+
					"bypassed on this path:\n  %s", name, i+1, strings.TrimSpace(line))
			}
			if inEnterWrite && braceDepth <= 0 && strings.Contains(line, "}") {
				inEnterWrite = false
			}
		}
	}
}
