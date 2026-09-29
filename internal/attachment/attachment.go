// Package attachment exposes the CLI-shaped facade for managing
// entity file attachments (attach, list). The service depends only
// on the focused primitives it needs (Store, Meta, EntityManager) so
// it can be constructed at any wiring site.
package attachment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/store"
)

// Info describes a single file attachment on an entity.
type Info struct {
	Property    string
	Path        string
	FileName    string
	ContentType string
	Size        int64
}

// Result is the outcome of [Service.Attach] and [Service.WriteAttachment].
type Result struct {
	Path     string
	FileName string

	// Entity is the post-write entity as stored, unredacted. A caller that
	// serves it must apply its own read-side redaction.
	Entity *entity.Entity
}

// EntityPatcher is the write surface the attachment service needs: recording
// an attachment changes exactly one property, the file property's stamped
// path(s). See the internal/entitymanager package doc for the consumer-side
// rule this follows (TKT-IVSJV6).
//
// PatchEntity rather than UpdateEntity: a whole-entity save would hold the
// record across the processor run (a scan may take up to a minute) and
// overwrite any property edited meanwhile. A patch names only the file
// property, so the service needs the entity's identity, never an owned full
// record, and a caller may hand it an entity read through a redacting reader.
type EntityPatcher interface {
	PatchEntity(ctx context.Context, id string, p entity.Patch) (*entity.UpdateResult, error)
}

// Locker is the keyed lock the service takes around each write to one
// (entity, property). internal/lock supplies the implementations: a
// BackendLocker when several processes share one store, a MemoryLocker
// otherwise.
type Locker interface {
	// Acquire blocks until key is held or ctx is done, and returns an
	// idempotent release. A wait cut short by ctx returns an error wrapping
	// ctx.Err().
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// WriteAuthorizer decides whether the caller in ctx may still change e's
// attachments. The service asks it under the property lock, right before the
// store is touched: callers authorize up front too, but a slow upload or scan
// separates that check from the write, and the entity's state or the caller's
// grants may change in between. It returns nil to allow.
type WriteAuthorizer interface {
	AuthorizeAttachmentWrite(ctx context.Context, e *entity.Entity) error
}

// AllowAllWrites is the named opt-out [WriteAuthorizer] for callers on the
// operator's shell (the CLI), which has no ACL to consult.
type AllowAllWrites struct{}

// AuthorizeAttachmentWrite implements [WriteAuthorizer] and allows every write.
func (AllowAllWrites) AuthorizeAttachmentWrite(context.Context, *entity.Entity) error { return nil }

// TwinOwnership answers which fields of an entity an external system owns
// through a twin, keyed by field name ("*" for every field) → owning
// systems. Defined at the call site; the twins service and
// [entitymanager.NoTwinOwnership] satisfy it.
type TwinOwnership interface {
	OwnedFields(ctx context.Context, entityType, entityID string) (map[string][]string, error)
}

// Deps is the dependency bundle [New] requires. Store, Meta, EntityManager,
// Locker, Authorizer and Twins are mandatory; [New] returns an error if any is
// nil. Processor is optional — when nil the service uses [NoopProcessor].
type Deps struct {
	Store         store.Store
	Meta          *metamodel.Metamodel
	EntityManager EntityPatcher

	// Locker serializes writers to one (entity, property); see
	// [Service.WriteAttachment].
	Locker Locker

	// Authorizer re-checks each write under the property lock. Use
	// [AllowAllWrites] where there is no ACL.
	Authorizer WriteAuthorizer
	// Twins guards file properties an external system owns: an upload or
	// delete on one is refused before any bytes change. Pass
	// [entitymanager.NoTwinOwnership] where no pact is declared.
	Twins TwinOwnership

	// Processor inspects/rewrites attachment bytes before they are persisted
	// (scan, MIME validation, transform). Optional; defaults to [NoopProcessor].
	Processor Processor
}

// Service implements the attachment-facade methods that CLI invokes.
// Constructed once at the wiring site and shared across subcommands.
type Service struct {
	deps Deps
}

// New constructs a Service. Returns an error if any required
// dependency is nil — CLAUDE.md "constructors reject nil required
// fields."
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("attachment: Store is required")
	}
	if d.Meta == nil {
		return nil, errors.New("attachment: Meta is required")
	}
	if d.EntityManager == nil {
		return nil, errors.New("attachment: EntityManager is required")
	}
	if d.Locker == nil {
		return nil, errors.New("attachment: Locker is required")
	}
	if d.Authorizer == nil {
		return nil, errors.New("attachment: Authorizer is required")
	}
	if d.Twins == nil {
		return nil, errors.New("attachment: Twins is required")
	}
	if d.Processor == nil {
		d.Processor = NoopProcessor{}
	}
	return &Service{deps: d}, nil
}

// Attach streams the file at filePath into the store at
// `attachments/<entityID>/<property>/<fileName>` and records the path(s)
// on the entity's property. Behavior depends on the property's `max`: at
// max==1 (the default) the upload replaces the single attachment; above 1
// it appends up to the cap, auto-suffixing the name on collision and
// erroring when full.
//
// If property is empty, the first file-type property declared on the
// entity type (in alphabetical order — see [findFileProperty]) is
// used.
func (s *Service) Attach(ctx context.Context, entityID, filePath, property string) (*Result, error) {
	e, err := s.deps.Store.GetEntity(ctx, entityID)
	if err != nil {
		return nil, fmt.Errorf("get entity %s: %w", entityID, err)
	}

	entityDef, ok := s.deps.Meta.GetEntityDef(e.Type)
	if !ok {
		return nil, fmt.Errorf("unknown entity type: %s", e.Type)
	}

	propName := property
	if propName == "" {
		propName = findFileProperty(entityDef)
		if propName == "" {
			return nil, fmt.Errorf("no file property defined for entity type %s; specify property explicitly", e.Type)
		}
	}

	propDef, ok := entityDef.Properties[propName]
	if !ok {
		return nil, fmt.Errorf("property %q not defined for entity type %s", propName, e.Type)
	}
	if propDef.Type != metamodel.PropertyTypeFile {
		return nil, fmt.Errorf("property %q is not a file type (is %s)", propName, propDef.Type)
	}

	src, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()

	return s.WriteAttachment(ctx, e, propDef, propName, filepath.Base(filePath), src)
}

// Open returns the bytes of one attachment. The caller closes the reader and
// is responsible for ACL gating. A missing file wraps [store.ErrNotFound].
func (s *Service) Open(ctx context.Context, entityID, property, fileName string) (io.ReadCloser, error) {
	return s.deps.Store.ReadAttachment(ctx, entityID, property, fileName)
}

// fileProperty loads the raw entity and resolves property to a declared
// file-type property of its type.
func (s *Service) fileProperty(
	ctx context.Context, entityID, property string,
) (*entity.Entity, metamodel.PropertyDef, error) {
	e, err := s.deps.Store.GetEntity(ctx, entityID)
	if err != nil {
		return nil, metamodel.PropertyDef{}, fmt.Errorf("get entity %s: %w", entityID, err)
	}
	entityDef, ok := s.deps.Meta.GetEntityDef(e.Type)
	if !ok {
		return nil, metamodel.PropertyDef{}, fmt.Errorf("unknown entity type: %s", e.Type)
	}
	propDef, ok := entityDef.Properties[property]
	if !ok {
		return nil, metamodel.PropertyDef{}, fmt.Errorf("property %q not defined for entity type %s", property, e.Type)
	}
	if propDef.Type != metamodel.PropertyTypeFile {
		return nil, metamodel.PropertyDef{}, fmt.Errorf("property %q is not a file type (is %s)", property, propDef.Type)
	}
	return e, propDef, nil
}

// ErrAtCapacity is returned by [Service.WriteAttachment] when a multi-file
// property already holds its `max` attachments. Callers (the HTTP handler)
// map it to a 409.
var ErrAtCapacity = errors.New("attachment: property already holds the maximum number of attachments")

// WriteAttachment is the shared write-path policy used by both the CLI
// (Attach) and the data-entry HTTP upload handler, so the cap/suffix/
// stamp rules live in exactly one place. The entity, its property def, and
// the (already-gated) property name are passed in; r supplies the bytes.
//
// Ordering is deliberate to avoid data loss: the new file is written
// FIRST, and only after it lands are superseded files removed (at max==1,
// the other files on the property). A store failure mid-write therefore
// leaves the existing attachment intact.
//
// The cap (`max`) and the stamped file list are read-modify-writes over the
// property's file set, so the list-resolve-write-prune-stamp sequence runs
// under the keyed lock for (entity, property). It works across processes
// when Deps.Locker is backed by the shared store. The slow steps stay outside the
// lock: the processor (a scan may take up to a minute) and reading r, which
// is spooled to a temporary file first, so a slow client cannot hold the lock.
func (s *Service) WriteAttachment(
	ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, propName, rawFileName string, r io.Reader,
) (*Result, error) {
	if err := s.rejectOwned(ctx, e, propName); err != nil {
		return nil, err
	}
	maxCount := propDef.FileMax()

	// Fail fast on a full property before reading or scanning any bytes. The
	// check is repeated under the lock below, where it counts.
	existing, err := s.attachmentFileNames(ctx, e.ID, propName)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	fileName, err := resolveAttachName(rawFileName, existing, maxCount)
	if err != nil {
		return nil, err
	}

	// Inspect / transform the bytes before persisting. A processor may buffer
	// (when it needs the full file), reject the upload (wrapping
	// ErrRejected), or rewrite the stream and the file name.
	pc := ProcessContext{EntityID: e.ID, EntityType: e.Type, Property: propName, FileName: fileName}
	r, fileName, err = runProcessor(ctx, s.deps.Processor, pc, r, store.MaxAttachmentBytes)
	if err != nil {
		return nil, err
	}
	spool, err := spoolAttachment(r)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	release, err := s.lockForWrite(ctx, e, propName)
	if err != nil {
		return nil, err
	}
	defer release()

	existing, err = s.attachmentFileNames(ctx, e.ID, propName)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	// Resolve against the set as it is now: a concurrent upload may have
	// filled the property or taken the name since the check above, and a
	// transform may have changed the name.
	fileName, err = resolveAttachName(fileName, existing, maxCount)
	if err != nil {
		return nil, err
	}

	// Write the new bytes first. On failure the existing files are untouched.
	if err = s.deps.Store.AttachFile(ctx, e.ID, propName, fileName, spool); err != nil {
		return nil, fmt.Errorf("store attachment: %w", err)
	}

	// Compute the post-write file set without a second ListAttachments: at
	// max==1 it's just the new file (and we delete the rest); above 1 it's
	// the prior set plus the new name (a same-name upload replaced in place,
	// so dedupe).
	var names []string
	if maxCount <= 1 {
		for _, old := range existing {
			if old == fileName {
				continue
			}
			_ = s.deps.Store.DeleteAttachment(ctx, e.ID, propName, old)
		}
		names = []string{fileName}
	} else {
		names = append(names, existing...)
		if !slices.Contains(names, fileName) {
			names = append(names, fileName)
		}
		sort.Strings(names)
	}

	key := attachPath(e.ID, propName, fileName)
	res, err := s.stamp(ctx, e, propName, maxCount, names)
	if err != nil {
		// The file landed on disk before the stamp ran, so a failure here
		// leaves an orphan. CleanupOrphanedTempFiles (analysis facade)
		// sweeps these — surface the path in the error so the operator can
		// find it.
		return nil, fmt.Errorf("update entity (attachment %s orphaned; run `rela gc --temp-files`): %w", key, err)
	}

	return &Result{Path: key, FileName: fileName, Entity: res.Entity}, nil
}

// lockWait bounds how long a writer waits for another writer to the same
// (entity, property). The holder never scans or reads an upload under the
// lock, but its stamp runs the entity's on-update automations, whose Lua may
// take up to the default script timeout (30s). The wait is twice that, so a
// holder that is still making progress does not cause a 503.
const lockWait = 60 * time.Second

// ErrBusy is returned when another writer held the same (entity, property)
// for longer than the service waits, and stands for a full [Limiter] at the
// upload handlers. Nothing was written; the caller may retry. The HTTP
// handler maps it to a 503.
var ErrBusy = errors.New("attachment: another write to this property is in progress")

// lockForWrite takes the property lock and then re-authorizes the write, so
// the decision holds for everything done under the lock. On a denial the lock
// is released and the authorizer's error returned.
func (s *Service) lockForWrite(ctx context.Context, e *entity.Entity, propName string) (func(), error) {
	release, err := s.lockProperty(ctx, e.ID, propName)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Authorizer.AuthorizeAttachmentWrite(ctx, e); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// lockProperty acquires the keyed lock serializing writers to one
// (entity, property). The deadline applies to the wait only; the returned
// release must be called once the write is done.
func (s *Service) lockProperty(ctx context.Context, entityID, propName string) (func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	release, err := s.deps.Locker.Acquire(waitCtx, "attachment/"+entityID+"/"+propName)
	if err != nil {
		// Only our own wait bound is "busy"; a caller that gave up is not.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("%w (%s/%s)", ErrBusy, entityID, propName)
		}
		return nil, fmt.Errorf("lock attachment property %s/%s: %w", entityID, propName, err)
	}
	return release, nil
}

// spoolAttachment copies r to a temporary file and rewinds it. The copy is
// capped at [store.MaxAttachmentBytes], the same limit every store enforces.
func spoolAttachment(r io.Reader) (*os.File, error) {
	f, err := os.CreateTemp("", "rela-attachment-*")
	if err != nil {
		return nil, fmt.Errorf("spool attachment: %w", err)
	}
	fail := func(err error) (*os.File, error) {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	if _, err := io.Copy(f, store.CapAttachmentReader(r, store.MaxAttachmentBytes)); err != nil {
		if errors.Is(err, store.ErrAttachmentTooLarge) {
			return fail(fmt.Errorf("store attachment: %w", err))
		}
		return fail(fmt.Errorf("spool attachment: %w", err))
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("spool attachment: %w", err))
	}
	return f, nil
}

// DeleteAttachment removes one file from a property and re-stamps it,
// shared with the HTTP delete handler. The caller gates the read; the write is
// authorized by Deps.Authorizer under the property lock. This performs the
// store delete + property re-stamp + persist.
// Deleting a file that is not there still re-stamps and succeeds.
func (s *Service) DeleteAttachment(
	ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, propName, fileName string,
) error {
	release, err := s.lockForWrite(ctx, e, propName)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.deleteFile(ctx, e, propDef, propName, fileName)
	return err
}

// deleteFile is [Service.DeleteAttachment], also reporting whether the file
// was there. The caller holds the property's lock.
func (s *Service) deleteFile(
	ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, propName, fileName string,
) (bool, error) {
	if err := s.rejectOwned(ctx, e, propName); err != nil {
		return false, err
	}
	err := s.deps.Store.DeleteAttachment(ctx, e.ID, propName, fileName)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("delete attachment: %w", err)
	}
	existed := err == nil
	names, err := s.attachmentFileNames(ctx, e.ID, propName)
	if err != nil {
		return false, fmt.Errorf("list attachments: %w", err)
	}
	// A retried delete changes nothing, so it must not write the entity: a
	// write is audited and runs `on: update` automations. A stale property
	// value is still repaired.
	if !existed && sameStamp(e.Properties[propName], stampValue(e.ID, propName, propDef.FileMax(), names)) {
		return false, nil
	}
	if _, err := s.stamp(ctx, e, propName, propDef.FileMax(), names); err != nil {
		return false, fmt.Errorf("update entity: %w", err)
	}
	return existed, nil
}

// ErrNoFileToDetach is returned by [Service.DetachFile] when no file name was
// given and the property holds no file, or several.
var ErrNoFileToDetach = errors.New("no file to detach")

// Detach removes one attachment from a file-type property of entityID; see
// [Service.DetachFile] for the fileName rules and the result.
func (s *Service) Detach(ctx context.Context, entityID, property, fileName string) (string, error) {
	e, propDef, err := s.fileProperty(ctx, entityID, property)
	if err != nil {
		return "", err
	}
	return s.DetachFile(ctx, e, propDef, property, fileName)
}

// DetachFile removes one attachment from a file-type property and returns
// the name of the file it removed. A named file is deleted idempotently: when
// it is not there, DetachFile succeeds and returns "". An empty fileName
// removes the property's only file, and errors when the property holds none
// or several (the caller must disambiguate). The caller gates the read; the
// write is authorized by Deps.Authorizer under the property lock.
func (s *Service) DetachFile(
	ctx context.Context, e *entity.Entity, propDef metamodel.PropertyDef, property, fileName string,
) (string, error) {
	release, err := s.lockForWrite(ctx, e, property)
	if err != nil {
		return "", err
	}
	defer release()
	if fileName == "" {
		existing, listErr := s.attachmentFileNames(ctx, e.ID, property)
		if listErr != nil {
			return "", fmt.Errorf("list attachments: %w", listErr)
		}
		switch len(existing) {
		case 0:
			return "", fmt.Errorf("%w: property %q has no attachment", ErrNoFileToDetach, property)
		case 1:
			fileName = existing[0]
		default:
			return "", fmt.Errorf("%w: property %q holds %d files; specify which to detach: %v",
				ErrNoFileToDetach, property, len(existing), existing)
		}
	}
	existed, err := s.deleteFile(ctx, e, propDef, property, fileName)
	if err != nil || !existed {
		return "", err
	}
	return fileName, nil
}

// attachmentFileNames lists the file names currently on the property, in
// stable order. A real store error is returned (not swallowed) because the
// write path makes cap / replace decisions from this list — acting on a
// degraded view could overshoot the cap or skip the replace-delete.
func (s *Service) attachmentFileNames(ctx context.Context, entityID, property string) ([]string, error) {
	infos, err := s.deps.Store.ListAttachments(ctx, entityID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, info := range infos {
		if info.Property == property {
			names = append(names, info.FileName)
		}
	}
	sort.Strings(names)
	return names, nil
}

// rejectOwned refuses any change to a file property an external system owns
// through a twin, as an [entitymanager.TheirsWriteError]. It runs before any
// store byte operation: the manager's own guard only sees the stamp patch,
// which comes after the bytes changed (and a same-name upload does not change
// the stamp at all). Unlike that guard it is not change-based — the bytes
// are the value, and every upload or delete changes them.
//
// A lookup error fails closed: "cannot tell who owns this" must not become
// "nobody owns this".
func (s *Service) rejectOwned(ctx context.Context, e *entity.Entity, propName string) error {
	owned, err := s.deps.Twins.OwnedFields(ctx, e.Type, e.ID)
	if err != nil {
		return fmt.Errorf("attachment: twin ownership of %s: %w", e.ID, err)
	}
	systems := append(slices.Clone(owned[propName]), owned[metamodel.PactAllFields]...)
	if len(systems) == 0 {
		return nil
	}
	slices.Sort(systems)
	return &entitymanager.TheirsWriteError{
		Type: e.Type, ID: e.ID, Fields: []string{propName}, Systems: slices.Compact(systems),
	}
}

// stamp records names as the property's value with a patch that names only
// that property (see [EntityPatcher]). e supplies identity only and is not
// modified.
func (s *Service) stamp(
	ctx context.Context, e *entity.Entity, property string, maxCount int, names []string,
) (*entity.UpdateResult, error) {
	patch := entity.Patch{Properties: map[string]any{property: stampValue(e.ID, property, maxCount, names)}}
	return s.deps.EntityManager.PatchEntity(ctx, entity.FormatStateRef(e.ID, e.Face), patch)
}

// stampValue is the property value for a known set of file names: a scalar
// path for a single-cap property (empty when none), a list of paths for a
// multi-cap property.
func stampValue(entityID, property string, maxCount int, names []string) any {
	if maxCount <= 1 {
		if len(names) == 0 {
			return ""
		}
		return attachPath(entityID, property, names[0])
	}
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, attachPath(entityID, property, n))
	}
	return paths
}

// sameStamp reports whether a stored property value equals want, a
// [stampValue] result. A list may load as []string or []any, and an unset
// value equals the empty stamp.
func sameStamp(stored, want any) bool {
	switch w := want.(type) {
	case string:
		s, _ := stored.(string)
		return s == w
	case []string:
		var got []string
		switch v := stored.(type) {
		case []string:
			got = v
		case []any:
			for _, x := range v {
				str, ok := x.(string)
				if !ok {
					return false
				}
				got = append(got, str)
			}
		case nil:
		default:
			return false
		}
		return slices.Equal(got, w)
	}
	return false
}

func attachPath(entityID, property, fileName string) string {
	return filepath.ToSlash(filepath.Join("attachments", entityID, property, fileName))
}

// resolveAttachName applies the filename policy for an attach: normalize
// (NormalizeFileName always yields a usable name, never ""), then at max>1
// auto-suffix on collision and return ErrAtCapacity when full. At max==1
// the name is whatever was uploaded (the caller replaces).
func resolveAttachName(rawName string, existing []string, maxCount int) (string, error) {
	name := store.NormalizeFileName(rawName)
	if maxCount <= 1 {
		return name, nil
	}
	if len(existing) >= maxCount {
		return "", ErrAtCapacity
	}
	have := make(map[string]bool, len(existing))
	for _, f := range existing {
		have[f] = true
	}
	// Auto-suffix on collision so a duplicate upload adds a distinct file.
	return store.SuffixOnCollision(name, func(c string) bool { return have[c] }), nil
}

// List returns all attachments for an entity. Content type is
// inferred from the filename extension on the fly; there is no
// persisted metadata sidecar.
func (s *Service) List(ctx context.Context, entityID string) ([]Info, error) {
	items, err := s.deps.Store.ListAttachments(ctx, entityID)
	if err != nil {
		return nil, err
	}

	infos := make([]Info, 0, len(items))
	for _, it := range items {
		path := filepath.ToSlash(filepath.Join("attachments", it.EntityID, it.Property, it.FileName))
		infos = append(infos, Info{
			Property:    it.Property,
			Path:        path,
			FileName:    it.FileName,
			ContentType: contentTypeForName(it.FileName),
			Size:        it.Size,
		})
	}
	return infos, nil
}
