package cli

import (
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/Sourcehaven-BV/rela/internal/acl"
	"github.com/Sourcehaven-BV/rela/internal/aclaudit"
	"github.com/Sourcehaven-BV/rela/internal/config"
	"github.com/Sourcehaven-BV/rela/internal/dataentryconfig"
	"github.com/Sourcehaven-BV/rela/internal/errors"
	"github.com/Sourcehaven-BV/rela/internal/metamodel"
	"github.com/Sourcehaven-BV/rela/internal/output"
)

// ACLCmd groups access-control commands.
type ACLCmd struct {
	Audit       ACLAuditCmd       `cmd:"" help:"Audit the ACL policy (acl.yaml) for misconfigurations."`
	WhoCan      ACLWhoCanCmd      `cmd:"" name:"who-can" help:"List every principal who can perform a verb on an entity, with the route each grant took."`
	Can         ACLCanCmd         `cmd:"" help:"Spot-check whether one principal can perform a verb on an entity; exits non-zero on deny."`
	CanRelation ACLCanRelationCmd `cmd:"" name:"can-relation" help:"Spot-check whether one principal can create/update/delete a relation of a type from an entity; exits non-zero on deny."`
	Map         ACLMapCmd         `cmd:"" help:"Map effective access across the graph — one principal (--principal) or every principal — by type with per-entity exceptions."`
}

// ACLAuditCmd runs the on-demand authorization-misconfiguration linter over the
// project's acl.yaml. It is advisory by default: it prints findings and exits 0.
// For CI, --fail-on=<severity> (or the --exit-code alias) makes the command exit
// non-zero when a finding at or above that severity is present. See
// internal/aclaudit (TKT-TS0J5K).
type ACLAuditCmd struct {
	// FailOn gates the exit code. Empty = never fail (advisory). A severity
	// label (critical|high|medium|low|nit) or "any" fails when a finding at or
	// above that level is present. "any" == "nit" (every finding).
	FailOn   string `help:"Exit non-zero when a finding at or above this severity is present: critical|high|medium|low|any. Empty = advisory (always exit 0)." enum:",critical,high,medium,low,any" default:""`
	ExitCode bool   `help:"Alias for --fail-on=high (fail CI on critical or high findings)."`
}

// Run executes `rela acl audit`.
func (c *ACLAuditCmd) Run(svc *readServices) error {
	threshold, gate, err := c.resolveFailOn()
	if err != nil {
		return err
	}

	policyPath := filepath.Join(svc.Paths.Root, "acl.yaml")
	policy, err := acl.LoadPolicy(policyPath)
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			out.WriteSuccess("No acl.yaml found; nothing to audit (the project has no access-control policy).")
			return nil
		}
		return fmt.Errorf("load acl.yaml: %w", err)
	}

	perms, permsErr := permissionConsumerFor(svc.Config, svc.Meta)
	if permsErr != nil {
		out.WriteWarning(
			"could not read %s (%v); skipping the dead-permission check, which cannot "+
				"tell a dead permission from one gating a document, card, navigation entry or command.",
			dataentryconfig.ConfigFile, permsErr)
	}

	findings := aclaudit.Audit(policy, &metamodelReader{m: svc.Meta}, perms)

	if out.Format == "json" {
		writeAuditJSON(findings)
	} else {
		writeAuditText(findings)
	}

	if gate && aclaudit.HasAtLeast(findings, threshold) {
		return errors.NewExitError(1)
	}
	return nil
}

// resolveFailOn computes the exit-code gate from --fail-on / --exit-code.
// Returns (threshold, gateEnabled, error). When gateEnabled is false the
// command is advisory and always exits 0. --exit-code is sugar for
// --fail-on=high; if both are set, --fail-on wins (the explicit threshold).
func (c *ACLAuditCmd) resolveFailOn() (aclaudit.Severity, bool, error) {
	if c.FailOn != "" {
		threshold, ok := aclaudit.ParseSeverity(c.FailOn)
		if !ok {
			return 0, false, fmt.Errorf("invalid --fail-on %q: want one of critical, high, medium, low, any", c.FailOn)
		}
		return threshold, true, nil
	}
	if c.ExitCode {
		return aclaudit.High, true, nil
	}
	return 0, false, nil
}

// writeAuditText renders findings as human-readable lines grouped by severity.
func writeAuditText(findings []aclaudit.Finding) {
	if len(findings) == 0 {
		out.WriteSuccess("ACL audit: no findings.")
		return
	}
	out.WriteWarning("ACL audit: %d finding(s)", len(findings))
	for _, f := range findings {
		out.WriteMessage("  [%s] %s (%s: %s)", f.Severity, f.Detail, f.Rule, f.Subject)
		if f.Fix != "" {
			out.WriteMessage("      fix: %s", f.Fix)
		}
	}
}

// auditFindingJSON is the per-finding JSON shape (severity as a label, not the
// internal int).
type auditFindingJSON struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

// writeAuditJSON emits findings via the shared AnalysisResult envelope.
func writeAuditJSON(findings []aclaudit.Finding) {
	details := make([]auditFindingJSON, 0, len(findings))
	for _, f := range findings {
		details = append(details, auditFindingJSON{
			Rule: f.Rule, Severity: f.Severity.String(), Subject: f.Subject,
			Detail: f.Detail, Fix: f.Fix,
		})
	}
	status, message := "success", "ACL audit: no findings"
	if len(findings) > 0 {
		status = "warning"
		message = fmt.Sprintf("ACL audit: %d finding(s)", len(findings))
	}
	_ = out.WriteAnalysisResult(output.AnalysisResult{
		Status: status, Message: message, Count: len(findings), Details: details,
	})
}

// metamodelReader adapts *metamodel.Metamodel to aclaudit.MetamodelReader. It
// lives here (the consumer side) rather than in internal/aclaudit, so aclaudit
// stays free of a metamodel dependency and bounded to the narrow lookups the
// audit actually uses.
type metamodelReader struct {
	m *metamodel.Metamodel
}

func (r *metamodelReader) HasEntityType(t string) bool {
	if r.m == nil {
		return false
	}
	return r.m.HasEntityType(t)
}

func (r *metamodelReader) GetRelation(name string) (aclaudit.RelationView, bool) {
	if r.m == nil {
		return aclaudit.RelationView{}, false
	}
	def, ok := r.m.GetRelationDef(name)
	if !ok {
		return aclaudit.RelationView{}, false
	}
	return aclaudit.RelationView{From: def.From}, true
}

// HasWorld reports whether the metamodel declares a world by this name.
//
// The implicit DEFAULT world is NOT declared and therefore reports false —
// the audit special-cases that name itself, which keeps "did the operator
// write this world in schema.yaml" a different question from "is this world
// usable". Case-sensitive, like every other name lookup; the loader rejects
// a declared world named "default" case-INSENSITIVELY, so no declared world
// can shadow the implicit one under any spelling.
//
// Reads the exported map rather than going through a Metamodel method: this
// adapter is the only consumer, and *Metamodel is already at its plimsoll
// exported-method cap.
func (r *metamodelReader) HasWorld(name string) bool {
	if r.m == nil {
		return false
	}
	_, ok := r.m.Worlds[name]
	return ok
}

// HasFace reports whether entity type t declares the content state named
// face. Resolves aliases via GetEntityDef, so a grant written against a
// type alias answers about the canonical type. A type with no `faces:`
// block declares none, including the empty one.
func (r *metamodelReader) HasFace(t, face string) bool {
	if r.m == nil {
		return false
	}
	def, ok := r.m.GetEntityDef(t)
	if !ok {
		return false
	}
	_, declared := def.Faces[face]
	return declared
}

// HasFaces reports whether entity type t declares any content states.
func (r *metamodelReader) HasFaces(t string) bool {
	if r.m == nil {
		return false
	}
	def, ok := r.m.GetEntityDef(t)
	if !ok {
		return false
	}
	return len(def.Faces) > 0
}

func (r *metamodelReader) HasField(t, field string) bool {
	if r.m == nil {
		return false
	}
	def, ok := r.m.GetEntityDef(t)
	if !ok {
		return false
	}
	_, ok = def.Properties[field]
	return ok
}

// EnumOptions returns the allowed values for an enum field on type t. A field
// is an enum either via inline `values:` or by referencing a custom enum type
// declared under the metamodel's top-level `types:`.
func (r *metamodelReader) EnumOptions(t, field string) ([]string, bool) {
	if r.m == nil {
		return nil, false
	}
	def, ok := r.m.GetEntityDef(t)
	if !ok {
		return nil, false
	}
	prop, ok := def.Properties[field]
	if !ok {
		return nil, false
	}
	if len(prop.Values) > 0 {
		return sortedClone(prop.Values), true
	}
	if ct, ok := r.m.Types[prop.Type]; ok && len(ct.Values) > 0 {
		return sortedClone(ct.Values), true
	}
	return nil, false
}

// sortedClone returns a sorted copy so the audit's "outside the enum" message
// is deterministic regardless of metamodel iteration.
func sortedClone(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

// dataEntryPermissions adapts the project's data-entry.yaml to
// aclaudit.PermissionConsumer. It lives here (the consumer side) rather than
// in internal/aclaudit, for the same reason metamodelReader does: aclaudit
// stays free of a dataentryconfig import and bounded to the one lookup the
// audit actually uses.
//
// A permission named by any of these surfaces is live config. Missing one
// makes the audit report a working grant as dead and advise removing it, so
// every permission-bearing field in dataentryconfig.Config must be collected
// here — see the per-surface tests.
type dataEntryPermissions struct {
	cfg *dataentryconfig.Config
	// meta supplies the SCHEMA-side consumers. A copy's `guard.permission`
	// gates publishing and lives in schema.yaml, not data-entry.yaml, so
	// collecting only the latter reported every copy guard as dead config and
	// advised deleting the permission that gates the copy.
	//
	// Nil is accepted: a project with no readable metamodel still gets the
	// data-entry half rather than losing the check entirely.
	meta *metamodel.Metamodel
}

// UsedPermissions returns every permission referenced by a data-entry UI gate:
// documents, dashboard cards, navigation entries (recursively, since groups
// nest items), commands and actions.
func (d *dataEntryPermissions) UsedPermissions() []string {
	if d == nil || d.cfg == nil {
		return nil
	}
	var perms []string
	add := func(perm string) {
		if perm != "" {
			perms = append(perms, perm)
		}
	}
	for _, doc := range d.cfg.Documents {
		add(doc.Permission)
	}
	for _, cmd := range d.cfg.Commands {
		add(cmd.Permission)
	}
	for _, action := range d.cfg.Actions {
		add(action.Permission)
	}
	if d.cfg.Dashboard != nil {
		for _, card := range d.cfg.Dashboard.Cards {
			add(card.Permission)
		}
	}
	perms = append(perms, navigationPermissions(d.cfg.Navigation)...)
	perms = append(perms, copyGuardPermissions(d.meta)...)
	return perms
}

// copyGuardPermissions returns the permission each declared copy is gated on.
//
// These are consumed by the copy kernel rather than by a UI gate, but they are
// referenced outside acl.yaml just the same, which is the only distinction the
// dead-permission check draws.
func copyGuardPermissions(m *metamodel.Metamodel) []string {
	if m == nil {
		return nil
	}
	perms := make([]string, 0, len(m.Copies))
	for _, def := range m.Copies {
		if def.Guard.Permission != "" {
			perms = append(perms, def.Guard.Permission)
		}
	}
	return perms
}

// navigationPermissions walks the navigation tree. Groups nest their entries
// under Items, so a flat loop over the top level would miss every permission
// on a grouped entry.
//
// It recurses even though NavigationEntry's own doc says nested groups are not
// supported: over-collecting a permission is harmless for an audit (it can only
// suppress an advisory finding), while under-collecting is the bug this whole
// consumer exists to fix.
func navigationPermissions(entries []dataentryconfig.NavigationEntry) []string {
	perms := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Permission != "" {
			perms = append(perms, e.Permission)
		}
		perms = append(perms, navigationPermissions(e.Items)...)
	}
	return perms
}

// permissionConsumerFor returns the audit's view of the permissions referenced
// by data-entry UI gates, or a nil INTERFACE when the config could not be read.
//
// The return type is the interface, not *dataEntryPermissions, on purpose. A
// helper returning the concrete type would force the caller to convert, and the
// obvious conversion (`var c PermissionConsumer = loaded` after an error, or
// assigning a nil pointer) yields a NON-nil interface wrapping a nil pointer —
// so aclaudit.Audit's `perms == nil` check would not fire and A7 would run
// blind, which is the exact false positive BUG-919PM6 fixed. Returning the
// interface makes the nil case unrepresentable any other way.
func permissionConsumerFor(cfg config.Loader, meta *metamodel.Metamodel) (aclaudit.PermissionConsumer, error) {
	loaded, err := loadDataEntryPermissions(cfg, meta)
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// loadDataEntryPermissions reads and parses data-entry.yaml for the audit's
// permission-consumer view. It deliberately does NOT validate the config: the
// audit's subject is acl.yaml, and a project whose data-entry.yaml is invalid
// should still get its ACL findings.
//
// A missing file returns a consumer over an empty config, NOT nil — "the
// project has no data-entry.yaml" is complete information (no UI gates exist),
// unlike "we could not look", which must suppress the dead-permission check.
//
// It reads through the injected [config.Loader] rather than the OS filesystem:
// that is the swap boundary for remote/embedded config backends, and reading
// around it would make the audit silently skip A7 on any deployment whose
// config does not live on local disk.
func loadDataEntryPermissions(cfg config.Loader, meta *metamodel.Metamodel) (*dataEntryPermissions, error) {
	data, err := cfg.Load(context.Background(), dataentryconfig.ConfigFile)
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			return &dataEntryPermissions{cfg: &dataentryconfig.Config{}, meta: meta}, nil
		}
		return nil, err
	}
	var parsed dataentryconfig.Config
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return &dataEntryPermissions{cfg: &parsed, meta: meta}, nil
}
