//go:build postgres

package pgstore_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Sourcehaven-BV/rela/internal/store"
)

// TestQueryTracer_FromPoolEmits proves the pool path: the tracer Open
// attaches sees every statement, and with slog Debug enabled queries emit
// log records. Without this, the tracer could be wired wrong and we'd ship
// one that nobody ever sees.
func TestQueryTracer_FromPoolEmits(t *testing.T) {
	// A lockedBuffer, not a bytes.Buffer: the store's listener goroutine logs
	// its startup queries (watermark priming, catch-up) through the same
	// default logger, possibly after the query below, so the read must
	// synchronize with it (BUG-1FFUSO).
	var buf lockedBuffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := openWriter(t, freshFeedSchema(t))

	// Drive one query through the store. The listener's own queries also
	// log here, so assert on this query's argument, which only it carries.
	const id = "tracer-pool-probe"
	_, _ = st.GetEntity(context.Background(), id)

	out := buf.String()
	require.Contains(t, out, "pgstore: query",
		"Open should attach the tracer and queries should emit at Debug")
	require.Contains(t, out, id, "the store's own query should be traced")
}

// TestQueryTracer_FromPoolRecordsStats proves per-request accounting
// end-to-end: a context carrying store.QueryStats sees exactly the
// statements the store issued, including those inside a Tx (pgx traces
// BEGIN/COMMIT through the same connection tracer, so a transaction costs
// more than its body — that is the honest number).
func TestQueryTracer_FromPoolRecordsStats(t *testing.T) {
	st := openWriter(t, freshFeedSchema(t))

	ctx, stats := store.WithQueryStats(context.Background())
	_, _ = st.GetEntity(ctx, "nonexistent")
	require.EqualValues(t, 1, stats.Queries(), "GetEntity is one round-trip")
	require.Positive(t, stats.Duration().Nanoseconds())

	before := stats.Queries()
	require.NoError(t, st.Tx(ctx, func(view store.Store) error {
		_, _ = view.GetEntity(ctx, "nonexistent")
		return nil
	}))
	require.Greater(t, stats.Queries(), before+1,
		"a Tx must account its body AND the transaction statements around it")

	// A context without stats must not disturb another request's counters.
	after := stats.Queries()
	_, _ = st.GetEntity(context.Background(), "nonexistent")
	require.Equal(t, after, stats.Queries())
}

// lockedBuffer is a bytes.Buffer safe for concurrent writes and reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
