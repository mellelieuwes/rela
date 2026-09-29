package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/output"
	"github.com/Sourcehaven-BV/rela/internal/principal"
	"github.com/Sourcehaven-BV/rela/internal/store"
	"github.com/Sourcehaven-BV/rela/internal/twins"
)

// TwinCmd is the `rela twin` command group: the agent's interface to twins,
// an entity's counterparts in external systems. rela does not call the
// external system itself (not in this stage); the agent reads and writes it
// with its own tools and reports here.
// See the twins package doc for the sync loop these verbs drive.
//
// Every verb runs under the operator's user with the tool stamped as
// [principal.ToolTwin], so the audit log attributes a sync write to the twin
// sync rather than to a hand edit through the CLI.
type TwinCmd struct {
	Link    TwinLinkCmd    `cmd:"" help:"Link an entity to its counterpart in an external system."`
	Unlink  TwinUnlinkCmd  `cmd:"" help:"Remove a twin; the entity is not touched."`
	Show    TwinShowCmd    `cmd:"" help:"Show the twins of one entity."`
	List    TwinListCmd    `cmd:"" help:"List twins."`
	Pending TwinPendingCmd `cmd:"" help:"List the twins that need the agent's attention, with the values to push."`
	Pull    TwinPullCmd    `cmd:"" help:"Reconcile what the agent read from the external item into rela."`
	Pushed  TwinPushedCmd  `cmd:"" help:"Confirm that the external item now holds rela's values at a version."`
	Gone    TwinGoneCmd    `cmd:"" help:"Mark a twin gone because the external item no longer exists."`
	Pact    TwinPactCmd    `cmd:"" help:"Print the pact between an entity type and a system (the agent's brief)."`
}

// TwinLinkCmd records a new twin.
type TwinLinkCmd struct {
	EntityID        string `arg:"" name:"entity-id" help:"Entity to link (e.g. SC-015)."`
	System          string `arg:"" help:"External system id, as declared under the type's pacts."`
	ExternalID      string `arg:"" name:"external-id" help:"The item's id in the external system."`
	URL             string `name:"url" required:"" help:"URL of the external item."`
	RemoteUpdatedAt string `name:"remote-updated-at" help:"The external item's modification time (RFC 3339)."`
}

// Run executes `rela twin link`.
func (c *TwinLinkCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	remoteAt, err := parseRemoteUpdatedAt(c.RemoteUpdatedAt)
	if err != nil {
		return err
	}
	twin, err := tw.Link(ctx, c.EntityID, c.System, c.ExternalID, c.URL, remoteAt)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &twinCLIError{msg: fmt.Sprintf("entity %s not found", c.EntityID), err: err}
	case err != nil:
		return twinError(err, c.System, c.ExternalID)
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(twinWire(twin))
	}
	out.WriteSuccess("Linked %s to %s/%s (%s, never synced)", twin.Target.ID, twin.System, twin.ExternalID, twin.State)
	return nil
}

// TwinUnlinkCmd removes a twin.
type TwinUnlinkCmd struct {
	System     string `arg:"" help:"External system id."`
	ExternalID string `arg:"" name:"external-id" help:"The item's id in the external system."`
}

// Run executes `rela twin unlink`.
func (c *TwinUnlinkCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	if err := tw.Unlink(ctx, c.System, c.ExternalID); err != nil {
		return twinError(err, c.System, c.ExternalID)
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(twinUnlinkedJSON{System: c.System, ExternalID: c.ExternalID, Unlinked: true})
	}
	out.WriteSuccess("Unlinked %s/%s", c.System, c.ExternalID)
	return nil
}

// TwinShowCmd lists the twins of one entity together with the fields each
// one owns.
type TwinShowCmd struct {
	EntityID string `arg:"" name:"entity-id" help:"Entity whose twins to show."`
}

// Run executes `rela twin show`.
func (c *TwinShowCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	list, err := tw.ForEntity(ctx, c.EntityID)
	if err != nil {
		return twinError(err, "", "")
	}
	owned := map[string][]string{}
	if len(list) > 0 {
		if owned, err = ownedNow(ctx, svc, tw, c.EntityID); err != nil {
			return err
		}
	}
	items := make([]twinShowJSON, len(list))
	for i, t := range list {
		items[i] = twinShowJSON{twinJSON: twinWire(t), OwnedFields: nonNil(owned[t.System])}
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(items)
	}
	if len(items) == 0 {
		out.WriteMessage("No twins for %s.", c.EntityID)
		return nil
	}
	for i, item := range items {
		if i > 0 {
			out.WriteMessage("")
		}
		writeTwinText(item)
	}
	return nil
}

// ownedNow maps each system to the fields of entityID it owns, resolving the
// pacts from the entity's CURRENT type, as the write guard does. The type a
// twin recorded at link time goes stale once the entity's type changes or is
// renamed, and `show` would then report nothing owned while writes are still
// refused. A missing entity owns nothing: ForEntity has marked its twins gone.
func ownedNow(
	ctx context.Context, svc *writeServices, tw *twins.Service, entityID string,
) (map[string][]string, error) {
	e, err := svc.Store.GetEntity(ctx, entityID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return map[string][]string{}, nil
	case err != nil:
		return nil, fmt.Errorf("rela twin: reading %s: %w", entityID, err)
	}
	byField, err := tw.OwnedFields(ctx, e.Type, e.ID)
	if err != nil {
		return nil, fmt.Errorf("rela twin: owned fields of %s: %w", entityID, err)
	}
	return ownedBySystem(byField), nil
}

// TwinListCmd lists twins, optionally filtered.
type TwinListCmd struct {
	System string `help:"Only twins in this system."`
	State  string `help:"Only twins in this state: pending, in_sync, conflict or gone." enum:",pending,in_sync,conflict,gone" default:""`
}

// Run executes `rela twin list`.
func (c *TwinListCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	f := twins.Filter{System: c.System}
	if c.State != "" {
		f.States = []twins.State{twins.State(c.State)}
	}
	list, err := tw.List(ctx, f)
	if err != nil {
		return twinError(err, "", "")
	}
	items := make([]twinJSON, len(list))
	for i, t := range list {
		items[i] = twinWire(t)
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(items)
	}
	if len(items) == 0 {
		out.WriteMessage("No twins.")
		return nil
	}
	tab := tabwriter.NewWriter(out.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tab, "SYSTEM\tEXTERNAL ID\tENTITY\tSTATE\tSYNCED\tFINDINGS")
	for _, item := range items {
		fmt.Fprintf(tab, "%s\t%s\t%s\t%s\t%s\t%d\n", item.System, item.ExternalID, item.Entity.ID, item.State,
			orNever(item.SyncedAt), len(item.Findings))
	}
	return tab.Flush()
}

// TwinPendingCmd prints the agent's work list.
type TwinPendingCmd struct {
	System string `help:"Only twins in this system."`
}

// Run executes `rela twin pending`.
func (c *TwinPendingCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	list, err := tw.Pending(ctx, c.System)
	if err != nil {
		return twinError(err, "", "")
	}
	items := make([]twinPendingJSON, len(list))
	for i, p := range list {
		items[i] = pendingWire(p)
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(items)
	}
	if len(items) == 0 {
		out.WriteMessage("Nothing pending.")
		return nil
	}
	for i, item := range items {
		if i > 0 {
			out.WriteMessage("")
		}
		out.WriteMessage("%s/%s  %s  %s", item.System, item.ExternalID, item.Entity.ID, item.State)
		out.WriteMessage("  reasons: %s", strings.Join(item.Reasons, "; "))
		if item.Version != "" {
			out.WriteMessage("  version: %s", item.Version)
		}
		for _, fv := range item.PushSet {
			out.WriteMessage("  push %s: %s -> %s", fv.Field, formatTwinValue(fv.Base), formatTwinValue(fv.Local))
		}
		writeFindingsText(item.Findings)
	}
	return nil
}

// TwinPullCmd reconciles a remote document into rela.
type TwinPullCmd struct {
	System          string `arg:"" help:"External system id."`
	ExternalID      string `arg:"" name:"external-id" help:"The item's id in the external system."`
	Remote          string `required:"" help:"JSON file with the translated external values, or - for stdin."`
	RemoteUpdatedAt string `name:"remote-updated-at" required:"" help:"The external item's modification time as read (RFC 3339)."`
	Force           bool   `help:"Accept a pull older than the twin's stored modification time."`
}

// Run executes `rela twin pull`. A sync write rela refuses on its merits is
// not an error: the twin records a rejected finding, which is printed.
func (c *TwinPullCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	remoteAt, err := parseRemoteUpdatedAt(c.RemoteUpdatedAt)
	if err != nil {
		return err
	}
	remote, err := readRemote(c.Remote)
	if err != nil {
		return err
	}
	res, err := tw.Pull(ctx, c.System, c.ExternalID, remote, remoteAt, c.Force)
	if err != nil {
		return twinError(err, c.System, c.ExternalID)
	}
	item := twinPullJSON{twinJSON: twinWire(res.Twin), Applied: nonNil(res.Applied)}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(item)
	}
	switch {
	case item.State == string(twins.StateGone):
		out.WriteWarning("%s no longer exists; %s/%s is marked gone", item.Entity.ID, item.System, item.ExternalID)
	case len(item.Applied) == 0:
		out.WriteSuccess("Pulled %s/%s into %s: nothing to write (%s)", item.System, item.ExternalID, item.Entity.ID,
			item.State)
	default:
		out.WriteSuccess("Pulled %s/%s into %s: wrote %s (%s)", item.System, item.ExternalID, item.Entity.ID,
			strings.Join(item.Applied, ", "), item.State)
	}
	for _, f := range item.Findings {
		if f.Kind == string(twins.FindingRejected) {
			out.WriteWarning("rela refused the sync write: %s", f.Message)
		}
	}
	writeFindingsText(item.Findings)
	return nil
}

// TwinPushedCmd records a completed push.
type TwinPushedCmd struct {
	System          string `arg:"" help:"External system id."`
	ExternalID      string `arg:"" name:"external-id" help:"The item's id in the external system."`
	Version         string `required:"" help:"The entity version from the pending item that was pushed."`
	RemoteUpdatedAt string `name:"remote-updated-at" help:"The external item's modification time after the push (RFC 3339); without it the stored time is kept."`
}

// Run executes `rela twin pushed`.
func (c *TwinPushedCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	remoteAt, err := parseRemoteUpdatedAt(c.RemoteUpdatedAt)
	if err != nil {
		return err
	}
	twin, err := tw.Pushed(ctx, c.System, c.ExternalID, c.Version, remoteAt)
	if err != nil {
		return twinError(err, c.System, c.ExternalID)
	}
	item := twinWire(twin)
	if out.Format == output.FormatJSON {
		return writeTwinJSON(item)
	}
	if item.State == string(twins.StateGone) {
		out.WriteWarning("%s no longer exists; %s/%s is marked gone", item.Entity.ID, item.System, item.ExternalID)
		return nil
	}
	out.WriteSuccess("Recorded push of %s/%s at version %s (%s)", item.System, item.ExternalID, c.Version, item.State)
	writeFindingsText(item.Findings)
	return nil
}

// TwinGoneCmd marks a twin gone.
type TwinGoneCmd struct {
	System     string `arg:"" help:"External system id."`
	ExternalID string `arg:"" name:"external-id" help:"The item's id in the external system."`
}

// Run executes `rela twin gone`.
func (c *TwinGoneCmd) Run(ctx context.Context, svc *writeServices) error {
	ctx, tw, err := twinSetup(ctx, svc)
	if err != nil {
		return err
	}
	twin, err := tw.MarkGone(ctx, c.System, c.ExternalID)
	if err != nil {
		return twinError(err, c.System, c.ExternalID)
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(twinWire(twin))
	}
	out.WriteSuccess("Marked %s/%s gone; unlink it once the entity is dealt with", twin.System, twin.ExternalID)
	return nil
}

// TwinPactCmd prints one pact, instructions included: the agent's brief.
type TwinPactCmd struct {
	EntityType string `arg:"" name:"entity-type" help:"Entity type (or alias) declaring the pact."`
	System     string `arg:"" help:"External system id."`
}

// Run executes `rela twin pact`.
func (c *TwinPactCmd) Run(ctx context.Context, svc *writeServices) error {
	if _, _, err := twinSetup(ctx, svc); err != nil {
		return err
	}
	pact, ok := metamodel.NewPactPolicy(svc.Meta).Pact(c.EntityType, c.System)
	if !ok {
		return &twinCLIError{
			msg: fmt.Sprintf("entity type %q has no pact with %q", c.EntityType, c.System),
			err: twins.ErrNoPact,
		}
	}
	item := twinPactJSON{
		EntityType:   pact.EntityType(),
		System:       pact.System(),
		Scope:        pact.Scope(),
		Theirs:       nonNil(pact.Theirs()),
		Shared:       nonNil(pact.Shared()),
		Ours:         oursFields(svc.Meta, pact),
		Propose:      nonNil(pact.Propose()),
		Instructions: pact.Instructions(),
	}
	if out.Format == output.FormatJSON {
		return writeTwinJSON(item)
	}
	out.WriteMessage("Pact: %s <-> %s", item.EntityType, item.System)
	out.WriteMessage("Scope:   %s", item.Scope)
	out.WriteMessage("Theirs:  %s", listOrNone(item.Theirs))
	out.WriteMessage("Shared:  %s", listOrNone(item.Shared))
	out.WriteMessage("Ours:    %s", listOrNone(item.Ours))
	out.WriteMessage("Propose: %s", listOrNone(item.Propose))
	if item.Instructions != "" {
		out.WriteMessage("")
		out.WriteMessage("Instructions:")
		out.WriteMessage("%s", strings.TrimRight(item.Instructions, "\n"))
	}
	return nil
}

// errNoPacts is every verb's answer on a project whose schema declares no
// pact: twins do not exist there, so there is nothing to link or sync.
var errNoPacts = errors.New("rela twin: no pacts declared in schema.yaml; " +
	"declare a pacts: block on an entity type to use twins")

// twinSetup returns the twin service and the context re-stamped with
// [principal.ToolTwin], keeping the user, so the audit log attributes sync
// writes to the twin sync.
func twinSetup(ctx context.Context, svc *writeServices) (context.Context, *twins.Service, error) {
	if svc.Twins == nil {
		return ctx, nil, errNoPacts
	}
	p := principal.From(ctx)
	p.Tool = principal.ToolTwin
	return principal.With(ctx, p), svc.Twins, nil
}

// twinCLIError is a twins failure re-worded for the operator. Error() is the
// clear message; Unwrap keeps the cause for errors.Is/As.
type twinCLIError struct {
	msg string
	err error
}

func (e *twinCLIError) Error() string { return e.msg }

func (e *twinCLIError) Unwrap() error { return e.err }

// twinError maps a twins failure to an operator-facing message. system and
// externalID name the twin the command addressed ("" when it addressed
// none). Errors it does not recognize pass through unchanged.
func twinError(err error, system, externalID string) error {
	key := system + "/" + externalID
	var conflict *store.VersionConflictError
	var msg string
	switch {
	case errors.Is(err, twins.ErrNoPact):
		msg = "entity type " + twinErrDetail(err, twins.ErrNoPact) + "; declare it under the type's pacts: in schema.yaml"
	case errors.Is(err, twins.ErrInvalidExternalID):
		msg = "invalid external id " + twinErrDetail(err, twins.ErrInvalidExternalID)
	case errors.Is(err, twins.ErrNotFound):
		msg = fmt.Sprintf("no twin %s; see rela twin list", key)
	case errors.Is(err, twins.ErrExternalIDTaken):
		msg = key + " is already linked; unlink it first"
	case errors.Is(err, twins.ErrDuplicateTarget):
		msg = fmt.Sprintf("the entity already has a twin in %s that is not gone; unlink that one first", system)
	case errors.Is(err, twins.ErrEntityLocked):
		msg = fmt.Sprintf("cannot link %s: it is locked by git-crypt; unlock the repository first",
			twinErrDetail(err, twins.ErrEntityLocked))
	case errors.Is(err, twins.ErrStalePull):
		msg = fmt.Sprintf("stale pull of %s (%s); pass --force to apply it anyway",
			key, twinErrDetail(err, twins.ErrStalePull))
	case errors.Is(err, twins.ErrVersionConflict):
		msg = fmt.Sprintf("the entity changed after the pushed values were read (%s); "+
			"run rela twin pending again and push the current values", twinErrDetail(err, twins.ErrVersionConflict))
	case errors.Is(err, twins.ErrGone):
		msg = key + " is gone; unlink it first"
	case errors.Is(err, twins.ErrStale):
		subject := key
		if system == "" {
			subject = "a twin"
		}
		msg = subject + " changed while this command ran (its entity was renamed, or another pull, push or gone " +
			"landed); the twin was not updated, run the command again"
	case errors.As(err, &conflict):
		msg = fmt.Sprintf("%s changed during the pull of %s; nothing was written, run it again", conflict.ID, key)
	default:
		return err
	}
	return &twinCLIError{msg: msg, err: err}
}

// twinErrDetail returns what the twins service appended to sentinel in err
// ("<sentinel>: <detail>"), or "" when it appended nothing.
func twinErrDetail(err, sentinel error) string {
	_, detail, _ := strings.Cut(err.Error(), sentinel.Error()+": ")
	return detail
}

// parseRemoteUpdatedAt parses an optional RFC 3339 flag value; empty is the
// zero time.
func parseRemoteUpdatedAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --remote-updated-at %q: want RFC 3339, e.g. 2026-09-20T14:03:11Z", s)
	}
	return t, nil
}

// remoteDoc is the wire shape of pull's input. Properties decodes into a map
// so a present null survives as a key with a nil value (cleared on their
// side), distinct from an absent key (not mapped). Body is raw for the same
// reason: absent is "not mapped", null is "cleared".
type remoteDoc struct {
	Properties map[string]any  `json:"properties"`
	Body       json.RawMessage `json:"body"`
}

// readRemote reads and decodes pull's remote document from path, or stdin
// for "-".
func readRemote(path string) (twins.Remote, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return twins.Remote{}, fmt.Errorf("reading --remote: %w", err)
	}
	return decodeRemote(raw)
}

// decodeRemote decodes a remote document strictly: an unknown top-level key
// is refused, because a misspelled "properties" would otherwise map nothing
// and the pull would silently agree with rela.
func decodeRemote(raw []byte) (twins.Remote, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc remoteDoc
	if err := dec.Decode(&doc); err != nil {
		return twins.Remote{}, fmt.Errorf("remote JSON: %w (want {\"properties\": {...}, \"body\": \"...\"})", err)
	}
	if dec.More() {
		return twins.Remote{}, errors.New("remote JSON: unexpected data after the document")
	}
	if _, ok := doc.Properties[metamodel.PactBodyField]; ok {
		return twins.Remote{}, errors.New(`remote JSON: "body" is not a property; send it as the top-level "body" key`)
	}
	remote := twins.Remote{Properties: doc.Properties}
	switch {
	case doc.Body == nil:
		// absent: not mapped
	case string(doc.Body) == "null":
		cleared := ""
		remote.Body = &cleared
	default:
		var body string
		if err := json.Unmarshal(doc.Body, &body); err != nil {
			return twins.Remote{}, errors.New(`remote JSON: "body" must be a string or null`)
		}
		remote.Body = &body
	}
	return remote, nil
}

// Wire shapes of `rela twin -o json`. Explicit structs with snake_case keys
// so the JSON contract agents parse never follows a rename in the twins
// package.
type (
	twinEntityJSON struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}

	twinFindingJSON struct {
		Field   string `json:"field"`
		Kind    string `json:"kind"`
		Base    any    `json:"base"`
		Ours    any    `json:"ours"`
		Theirs  any    `json:"theirs"`
		Propose bool   `json:"propose"`
		Message string `json:"message"`
	}

	twinFieldValueJSON struct {
		Field string `json:"field"`
		Base  any    `json:"base"`
		Local any    `json:"local"`
	}

	// twinJSON is one twin. Version is the entity version the twin last
	// agreed at.
	twinJSON struct {
		System          string            `json:"system"`
		ExternalID      string            `json:"external_id"`
		URL             string            `json:"url"`
		Entity          twinEntityJSON    `json:"entity"`
		State           string            `json:"state"`
		HasBase         bool              `json:"has_base"`
		Version         string            `json:"version,omitempty"`
		SyncedAt        string            `json:"synced_at,omitempty"`
		RemoteUpdatedAt string            `json:"remote_updated_at,omitempty"`
		Findings        []twinFindingJSON `json:"findings"`
	}

	twinShowJSON struct {
		twinJSON
		OwnedFields []string `json:"owned_fields"`
	}

	twinPullJSON struct {
		twinJSON
		Applied []string `json:"applied"`
	}

	twinPendingJSON struct {
		System       string               `json:"system"`
		ExternalID   string               `json:"external_id"`
		URL          string               `json:"url"`
		Entity       twinEntityJSON       `json:"entity"`
		State        string               `json:"state"`
		Reasons      []string             `json:"reasons"`
		LocalChanged bool                 `json:"local_changed"`
		Version      string               `json:"version"`
		PushSet      []twinFieldValueJSON `json:"push_set"`
		Findings     []twinFindingJSON    `json:"findings"`
	}

	twinUnlinkedJSON struct {
		System     string `json:"system"`
		ExternalID string `json:"external_id"`
		Unlinked   bool   `json:"unlinked"`
	}

	twinPactJSON struct {
		EntityType   string   `json:"entity_type"`
		System       string   `json:"system"`
		Scope        string   `json:"scope"`
		Theirs       []string `json:"theirs"`
		Shared       []string `json:"shared"`
		Ours         []string `json:"ours"`
		Propose      []string `json:"propose"`
		Instructions string   `json:"instructions"`
	}
)

func twinWire(t twins.Twin) twinJSON {
	return twinJSON{
		System:          t.System,
		ExternalID:      t.ExternalID,
		URL:             t.URL,
		Entity:          twinEntityJSON{Type: t.Target.Type, ID: t.Target.ID},
		State:           string(t.State),
		HasBase:         t.HasBase,
		Version:         t.BaseVersion,
		SyncedAt:        rfc3339(t.SyncedAt),
		RemoteUpdatedAt: rfc3339(t.RemoteUpdatedAt),
		Findings:        findingsWire(t.Findings),
	}
}

func pendingWire(p twins.PendingItem) twinPendingJSON {
	push := make([]twinFieldValueJSON, len(p.PushSet))
	for i, fv := range p.PushSet {
		push[i] = twinFieldValueJSON{Field: fv.Field, Base: fv.Base, Local: fv.Local}
	}
	return twinPendingJSON{
		System:       p.Twin.System,
		ExternalID:   p.Twin.ExternalID,
		URL:          p.Twin.URL,
		Entity:       twinEntityJSON{Type: p.Twin.Target.Type, ID: p.Twin.Target.ID},
		State:        string(p.Twin.State),
		Reasons:      nonNil(p.Reasons),
		LocalChanged: p.LocalChanged,
		Version:      p.Version,
		PushSet:      push,
		Findings:     findingsWire(p.Twin.Findings),
	}
}

func findingsWire(fs []twins.Finding) []twinFindingJSON {
	out := make([]twinFindingJSON, len(fs))
	for i, f := range fs {
		out[i] = twinFindingJSON{
			Field: f.Field, Kind: string(f.Kind), Base: f.Base, Ours: f.Ours, Theirs: f.Theirs,
			Propose: f.Propose, Message: f.Message,
		}
	}
	return out
}

// rfc3339 formats t, or "" for the zero time so the key is omitted.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ownedBySystem inverts OwnedFields' field→systems map into system→fields,
// each list sorted.
func ownedBySystem(byField map[string][]string) map[string][]string {
	out := map[string][]string{}
	for field, systems := range byField {
		for _, s := range systems {
			out[s] = append(out[s], field)
		}
	}
	for _, fields := range out {
		sort.Strings(fields)
	}
	return out
}

// oursFields lists the fields of the pact's entity type rela owns: every
// declared property the pact does not hand out, and the body unless it is
// handed out, sorted.
func oursFields(meta *metamodel.Metamodel, pact metamodel.Pact) []string {
	ours := []string{}
	def, ok := meta.GetEntityDef(pact.EntityType())
	if !ok {
		return ours
	}
	for name := range def.Properties {
		if pact.Owner(name) == metamodel.OwnerOurs {
			ours = append(ours, name)
		}
	}
	if pact.Owner(metamodel.PactBodyField) == metamodel.OwnerOurs {
		ours = append(ours, metamodel.PactBodyField)
	}
	slices.Sort(ours)
	return ours
}

func writeTwinJSON(v any) error {
	enc := json.NewEncoder(out.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeTwinText prints one shown twin as a short block.
func writeTwinText(t twinShowJSON) {
	out.WriteMessage("%s/%s  %s  %s", t.System, t.ExternalID, t.Entity.ID, t.State)
	out.WriteMessage("  url: %s", t.URL)
	out.WriteMessage("  synced: %s  remote updated: %s", orNever(t.SyncedAt), orNever(t.RemoteUpdatedAt))
	out.WriteMessage("  owned by %s: %s", t.System, listOrNone(t.OwnedFields))
	writeFindingsText(t.Findings)
}

func writeFindingsText(fs []twinFindingJSON) {
	for _, f := range fs {
		field := f.Field
		if field == "" {
			field = "(entity)"
		}
		if f.Kind == string(twins.FindingRejected) {
			out.WriteMessage("  finding %s %s: %s", f.Kind, field, f.Message)
			continue
		}
		propose := ""
		if f.Propose {
			propose = " (propose)"
		}
		out.WriteMessage("  finding %s %s%s: base %s, rela %s, theirs %s", f.Kind, field, propose,
			formatTwinValue(f.Base), formatTwinValue(f.Ours), formatTwinValue(f.Theirs))
	}
}

// formatTwinValue renders a field value compactly for text output: JSON, so
// strings are quoted and an absent value reads as null.
func formatTwinValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func orNever(s string) string {
	if s == "" {
		return "never"
	}
	return s
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}
