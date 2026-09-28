package attachment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Processor inspects and optionally rewrites an attachment's bytes on the
// write path, just before they are persisted. It is the single seam through
// which virus scanning, MIME validation, and byte transforms (metadata strip,
// resize) interpose.
//
// This is a consumer-side interface (CLAUDE.md: declared where it is used, the
// `attachment` package, not next to an implementation). The wiring site
// supplies a concrete processor; when [Deps.Processor] is nil the service uses
// [NoopProcessor], so the no-processor path never buffers in memory.
//
// Process receives the upload context and a reader over the bytes. It returns
// either a (possibly-rewritten) reader plus the resulting [ProcessInfo], or a
// rejection error. A processor that only validates (scan, allowlist) returns
// the input reader unchanged; a processor that transforms (strip, resize)
// returns a fresh reader over the new bytes and may set [ProcessInfo.FileName]
// when the transform changes the extension.
//
// A rejection (virus found, disallowed type) is returned as an error wrapping
// [ErrRejected] so callers can map it to a 4xx rather than a 5xx.
type Processor interface {
	// NeedsFullFile reports whether this processor must see the complete file
	// (e.g. a scanner streaming to clamd, or an image decoder). When true the
	// seam buffers the upload up to the size cap before calling Process; when
	// false the reader may be streamed. A processor that wraps the stream
	// lazily can return false; most real processors return true.
	NeedsFullFile() bool

	// Process inspects/rewrites the bytes for one attachment. It must not
	// retain the returned reader after the caller has consumed it. Returning an
	// error wrapping [ErrRejected] signals a policy rejection (4xx); any other
	// error is treated as an internal failure (5xx).
	Process(ctx context.Context, in ProcessContext, r io.Reader) (io.Reader, ProcessInfo, error)
}

// ProcessContext describes the attachment being written, so a processor can
// make per-field / per-type decisions without reaching back into the store.
// All fields are populated by the seam before the byte stream is offered.
type ProcessContext struct {
	EntityID   string // owning entity ID
	EntityType string // owning entity type name
	Property   string // the file-type property receiving the attachment
	FileName   string // the resolved (normalized, collision-suffixed) file name
}

// ProcessInfo is what a [Processor] reports back about the processed bytes.
// A pure validator leaves it zero; a transform may set FileName when it changes
// the file (e.g. a HEIC→JPEG convert).
type ProcessInfo struct {
	// FileName, when non-empty, replaces the stored file name (e.g. a transform
	// that changed the extension). Empty means keep the resolved name.
	FileName string
}

// ErrRejected wraps a processor's policy refusal (infected file, disallowed
// MIME type, …) so callers distinguish a deliberate 4xx rejection from an
// internal 5xx failure. Use [Rejectedf] to construct one.
var ErrRejected = errors.New("attachment: rejected by processor")

// RejectionReason returns the human-readable reason of an error wrapping
// [ErrRejected], without the sentinel's prefix.
func RejectionReason(err error) string {
	return strings.TrimPrefix(err.Error(), ErrRejected.Error()+": ")
}

// Rejectedf builds a rejection error wrapping [ErrRejected] with a
// caller-facing message.
func Rejectedf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRejected}, args...)...)
}

// NoopProcessor is the default [Processor]: it validates nothing, transforms
// nothing, and passes the reader through untouched. Because it does not need
// the full file, the seam streams it without buffering in memory.
type NoopProcessor struct{}

// NeedsFullFile reports false — the no-op never buffers.
func (NoopProcessor) NeedsFullFile() bool { return false }

// Process returns the input reader unchanged.
func (NoopProcessor) Process(_ context.Context, _ ProcessContext, r io.Reader) (io.Reader, ProcessInfo, error) {
	return r, ProcessInfo{}, nil
}

// runProcessor applies the service's processor to one attachment's bytes,
// buffering the stream first when the processor needs the whole file. It
// returns the reader to persist, the (possibly-updated) file name, and any
// error. When the processor is the no-op (or needs no buffering) the original
// reader is threaded straight through, unbuffered. (The service still spools
// it to a temporary file before taking the property lock; see
// spoolAttachment.)
//
// maxBytes bounds the buffer so a processor that needs the full file cannot be
// used to exhaust memory; it should be the same cap the ingress layer enforces.
func runProcessor(
	ctx context.Context, p Processor, pc ProcessContext, r io.Reader, maxBytes int64,
) (io.Reader, string, error) {
	if p == nil {
		p = NoopProcessor{}
	}

	src := r
	if p.NeedsFullFile() {
		// Buffer up to maxBytes+1 so we can tell "exactly at cap" from "over".
		// The ingress MaxBytesReader / CapAttachmentReader already bound the
		// stream, so this read is itself bounded; the +1 is a defensive backstop.
		buf, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
		if err != nil {
			return nil, "", fmt.Errorf("buffer attachment for processing: %w", err)
		}
		if int64(len(buf)) > maxBytes {
			return nil, "", fmt.Errorf("attachment exceeds processing size cap (%d bytes)", maxBytes)
		}
		src = bytes.NewReader(buf)
	}

	out, info, err := p.Process(ctx, pc, src)
	if err != nil {
		return nil, "", err
	}
	fileName := pc.FileName
	if info.FileName != "" {
		fileName = info.FileName
	}
	return out, fileName, nil
}
