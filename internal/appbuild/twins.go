package appbuild

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/project"
	"github.com/Sourcehaven-BV/rela/internal/storage"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/twins"
	"github.com/Sourcehaven-BV/rela/internal/twins/filetwins"
)

// twinsDirName is the twin store's home inside the project's `.rela/`
// directory. Twins are machine state about the sync, not graph content, so
// they sit beside comments rather than beside `entities/`, and a project
// that declares no pact finds nothing there.
const twinsDirName = "twins"

// buildTwins constructs the twin service and the late-bound writer it syncs
// through.
//
// Nil: both results are nil when the metamodel declares no pact — a genuinely
// nil *twins.Service, the "feature off" signal buildComments uses, so a
// project without pacts gets no directory, no alias subscriber and
// [entitymanager.NoTwinOwnership] as its guard.
//
// dbBackend names the recipe's database backend when it has one (see
// twinStoreGap). Only a file store exists in stage 1, and filetwins is
// node-local: under postgres a twin linked through one node would be invisible
// to the others, and under sqlite it would not travel with rela.db. So a
// database recipe with pacts is refused here rather than falling back.
//
// The writer comes back unbound because of a construction cycle: the service
// is the manager's twin guard (Deps.Twins) and alias subscriber, while the
// service's SyncWriter is the manager's twin-sync handle. The caller binds it
// once the manager exists ([twinSyncWriter.bind]).
func buildTwins(
	fs storage.FS, paths *project.Context, meta *metamodel.Metamodel, st store.Store, dbBackend string,
) (*twins.Service, *twinSyncWriter, error) {
	if !metamodel.NewPactPolicy(meta).Enabled() {
		return nil, nil, nil
	}
	if dbBackend != "" {
		return nil, nil, fmt.Errorf(
			"twins: the schema declares pacts, but the %s backend has no twin store yet; "+
				"twins run on the filesystem backends only (remove the pacts or use the fs build)", dbBackend)
	}
	if fs == nil || paths == nil || paths.CacheDir == "" {
		// Same stance as buildComments: a configured feature with nowhere to
		// keep its data is a wiring failure, not something to switch off.
		return nil, nil, errors.New("twins: pacts are declared but no project cache directory is available")
	}
	twinStore, err := filetwins.New(fs, filepath.Join(paths.CacheDir, twinsDirName))
	if err != nil {
		return nil, nil, fmt.Errorf("twins: %w", err)
	}
	writer := &twinSyncWriter{}
	svc, err := twins.NewService(twinStore, func() *metamodel.Metamodel { return meta },
		twinEntityReader{st: st}, writer, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("twins: %w", err)
	}
	return svc, writer, nil
}

// twinOwnership is the manager's twin guard for svc: the service itself, or
// the named opt-out when twins are off. A nil *twins.Service must not reach
// [entitymanager.Deps.Twins] boxed in the interface — New would accept the
// non-nil interface and the first write would dereference nil.
func twinOwnership(svc *twins.Service) entitymanager.TwinOwnership {
	if svc == nil {
		return entitymanager.NoTwinOwnership{}
	}
	return svc
}

// Twins returns the twin service, or nil when the metamodel declares no
// pact (or svc is nil). Callers must nil-check.
//
// A package function rather than a Services method because Services sits at
// its plimsoll exported-method cap (see [CompiledWorlds]).
func Twins(svc *Services) *twins.Service {
	if svc == nil {
		return nil
	}
	return svc.twins
}

// TwinOwnership returns svc's twin ownership guard: the twin service, or
// [entitymanager.NoTwinOwnership] when no pact is declared. For wiring
// sites that need the guard outside the manager (the attachment service),
// so none of them re-derives the nil opt-out.
func TwinOwnership(svc *Services) entitymanager.TwinOwnership {
	return twinOwnership(Twins(svc))
}

// twinSyncWriter is the twin service's [twins.SyncWriter], bound to the
// manager's twin-sync handle after the manager is built (see buildTwins for
// the cycle it breaks). Binding happens during single-threaded wiring, before
// the Services bundle is published, so no lock guards it.
type twinSyncWriter struct {
	m *entitymanager.Manager
}

// bind attaches the manager whose [entitymanager.TwinSyncWriter] handle
// performs the sync writes. A nil writer (twins off) has nothing to bind.
func (w *twinSyncWriter) bind(m *entitymanager.Manager) {
	if w == nil {
		return
	}
	w.m = entitymanager.TwinSyncWriter(m)
}

// PatchEntity implements [twins.SyncWriter]. Unbound, it refuses rather than
// panicking: that can only mean a wiring bug, and the sync must fail loudly.
func (w *twinSyncWriter) PatchEntity(
	ctx context.Context, id string, p entity.Patch,
) (*entity.UpdateResult, error) {
	if w.m == nil {
		return nil, errors.New("appbuild: twin sync writer used before the entity manager was bound")
	}
	return w.m.PatchEntity(ctx, id, p)
}

// twinEntityReader is the twin service's [twins.EntityReader] over the raw
// store. Raw on purpose, like entitymanager's write-prep read: the service
// snapshots the whole entity as the sync base, and a redacted read would
// record hidden values as absent.
type twinEntityReader struct {
	st store.Store
}

// GetEntityVersion returns the stored entity and the version token of
// exactly that row, so a later compare-and-swap describes what was read.
func (r twinEntityReader) GetEntityVersion(ctx context.Context, id string) (*entity.Entity, string, error) {
	e, err := r.st.GetEntity(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", twinEntityNotFoundError{id: id, err: err}
		}
		return nil, "", err
	}
	return e, string(store.VersionOf(e)), nil
}

// twinEntityNotFoundError carries the structural EntityNotFound marker the twins
// service classifies a missing entity by (it imports neither store nor
// entitymanager). errors.Is(err, store.ErrNotFound) keeps working.
type twinEntityNotFoundError struct {
	id  string
	err error
}

func (e twinEntityNotFoundError) Error() string { return "entity not found: " + e.id }

func (e twinEntityNotFoundError) Unwrap() error { return e.err }

// EntityNotFound marks the entity-does-not-exist condition.
func (e twinEntityNotFoundError) EntityNotFound() bool { return true }
