package dataentry

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/attachment"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

func TestWriteAttachmentBusy(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/tickets/T-1/_attachments/spec", http.NoBody)

	rec := httptest.NewRecorder()
	if !writeAttachmentBusy(rec, req, fmt.Errorf("wrapped: %w", attachment.ErrBusy)) {
		t.Fatal("ErrBusy was not handled")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}

	rec = httptest.NewRecorder()
	if writeAttachmentBusy(rec, req, errors.New("other")) {
		t.Error("a non-busy error was handled as busy")
	}
}

func TestWritePatchError_ConflictStatus(t *testing.T) {
	conflict := fmt.Errorf("patch: %w", &store.VersionConflictError{})
	for _, tc := range []struct {
		name    string
		ifMatch string
		want    int
	}{
		{"caller precondition failed", `"abc"`, http.StatusPreconditionFailed},
		{"internal retries exhausted", "", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/tickets/T-1", http.NoBody)
			if tc.ifMatch != "" {
				req.Header.Set("If-Match", tc.ifMatch)
			}
			rec := httptest.NewRecorder()
			writePatchError(rec, req, conflict)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
