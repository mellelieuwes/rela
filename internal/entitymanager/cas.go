package entitymanager

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// casRetryAttempts bounds the manager's own compare-and-swap retry loops. Each
// lost attempt means another writer committed to the same row in between, and
// every round has a winner, so N concurrent writers to one row need at most N
// rounds. The bound covers the bursts the write paths produce (the webhook
// admits 8 in flight) with room to spare.
const casRetryAttempts = 16

// isVersionConflict reports whether err is a lost compare-and-swap.
func isVersionConflict(err error) bool {
	var conflict *store.VersionConflictError
	return errors.As(err, &conflict)
}

// maxBackoffShift caps the jitter window at 1ms<<5 = 32ms.
const maxBackoffShift = 5

// casBackoff waits a short random time before retry attempt+1, so writers
// that lost the same round do not all re-read and collide again at once. The
// window doubles per attempt up to 32ms. It returns early with ctx's error
// when ctx ends.
func casBackoff(ctx context.Context, attempt int) error {
	window := time.Millisecond << min(attempt, maxBackoffShift)
	//nolint:gosec // G404: jitter, not a secret
	t := time.NewTimer(rand.N(window))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeAutomationProperties persists an on-create automation's PropertiesSet
// onto a just-created entity and returns the row as written.
//
// It is a compare-and-swap against the stored row, not a write of the caller's
// in-memory copy: the row is re-read, only set is applied, and the write is
// pinned to the version read. A concurrent writer that patched the entity
// after its create keeps its change; the retry re-applies set on top of it.
//
// check runs on each attempt's candidate after computed properties are
// re-evaluated and before the write; the two callers differ in what they
// enforce there.
func writeAutomationProperties(
	ctx context.Context, deps Deps, created *entity.Entity, set map[string]string,
	check func(stored, next *entity.Entity) error,
) (*entity.Entity, error) {
	if err := rejectComputedPresent(deps, created.Type, stringMapAny(set)); err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		stored, err := deps.Store.GetEntityState(ctx, created.ID, created.Face)
		if err != nil {
			return nil, fmt.Errorf("read entity after automation: %w", err)
		}
		next := stored.Clone()
		for prop, val := range set {
			next.SetString(prop, val)
		}
		if evalErr := deps.Computed.Evaluate(ctx, next); evalErr != nil {
			return nil, evalErr
		}
		if check != nil {
			if checkErr := check(stored, next); checkErr != nil {
				return nil, checkErr
			}
		}
		// excludeSelfID is next.ID: the row is already persisted, so the
		// unique scan must not match the entity against itself.
		err = writeWithUniqueCheck(ctx, deps, next, next.ID, func(st store.Store) error {
			_, werr := st.UpdateEntityIf(ctx, next, store.UpdateCondition{
				ExpectedVersion: store.VersionOf(stored),
			})
			return werr
		})
		if err == nil {
			return next, nil
		}
		if isVersionConflict(err) && attempt < casRetryAttempts {
			if berr := casBackoff(ctx, attempt); berr != nil {
				return nil, berr
			}
			continue
		}
		if ok, mapped := mapUniquePropertyConflict(err); ok {
			return nil, mapped
		}
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			return nil, err
		}
		return nil, fmt.Errorf("write entity after automation: %w", err)
	}
}
