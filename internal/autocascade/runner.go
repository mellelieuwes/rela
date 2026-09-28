package autocascade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Sourcehaven-BV/rela/internal/audit"
	"github.com/Sourcehaven-BV/rela/internal/automation"
	"github.com/Sourcehaven-BV/rela/internal/entity"
)

// Runner executes the side effects of an automation cascade.
//
// Runner is constructed once (typically per Workspace or
// EntityManager) and invoked many times via [Runner.Process]. Each
// call supplies a [Host] (for entity/relation callbacks) and a
// [ScriptRunner] (for scripted-action execution) on the [Request];
// the per-call passing is what dissolves the future constructor
// cycle with EntityManager.
type Runner struct {
	engine *automation.Engine
}

// Deps is the constructor input for [New]. Using a struct keeps the
// constructor signature stable as Runner gains required collaborators
// (e.g. audit hooks via TKT-6YYM).
type Deps struct {
	// Engine is the rule-evaluation engine. Runner calls
	// engine.Process on each newly created entity in a cascade.
	Engine *automation.Engine
}

// New constructs a Runner. Required collaborators must be non-nil per
// the project's "constructors reject nil required fields" rule.
func New(d Deps) (*Runner, error) {
	if d.Engine == nil {
		return nil, errors.New("autocascade: New: Engine is required")
	}
	return &Runner{engine: d.Engine}, nil
}

// queueItem is one pending automation result to process during a BFS
// cascade. Internal — callers never construct these.
type queueItem struct {
	trigger    *entity.Entity
	autoResult *automation.Result
}

// Process runs the BFS automation cascade. It interprets req.Result's
// actions (set properties, create relations, create entities, run
// scripted actions), calling back into host for the structural
// operations and into req.Scripts for the scripted actions. Newly
// created entities are re-evaluated through the engine to discover
// further cascades, bounded by [MaxDepth].
//
// Behavior is preserved verbatim from the original
// workspace.applyAutomationSideEffects: the BFS order, the
// per-iteration action order (scripts → relations → entities), the
// error-continuation semantics across all action paths, and the
// depth-limit warning wording. The existing workspace cascade tests
// (AC3 in PLAN-V6UR) act as the regression check.
func (r *Runner) Process(ctx context.Context, host Host, req Request) (Outcome, error) {
	if req.Result == nil {
		return Outcome{}, nil
	}
	if req.Trigger == nil {
		return Outcome{}, errors.New("autocascade: Process: req.Trigger is required")
	}

	var outcome Outcome

	// BFS queue of pending automation results to process.
	queue := []queueItem{{req.Trigger, req.Result}}
	iterations := 0

	for len(queue) > 0 && iterations < MaxDepth {
		// Pop from front (BFS order — process all items at depth N
		// before depth N+1).
		item := queue[0]
		queue = queue[1:]
		iterations++

		// Process scripted actions for this trigger.
		//
		// Note: req.OldTrigger is reused for every queue item, not
		// just the initial one. This mirrors the pre-refactor
		// workspace.applyAutomationSideEffects behavior — for
		// cascaded entities the original trigger's old state flows
		// through. Preserving the behavior; not "fixing" it here.
		r.executeScriptActions(ctx, req.Scripts, req.Mutator,
			item.trigger, req.OldTrigger, item.autoResult.LuaToExecute, &outcome)

		// Process relations for this trigger.
		r.applyRelationCreations(ctx, host, item.trigger, item.autoResult.RelationsToCreate, &outcome)

		// Collect warnings/errors from this automation result.
		outcome.Warnings = append(outcome.Warnings, item.autoResult.Warnings...)
		outcome.Errors = append(outcome.Errors, item.autoResult.Errors...)

		// Process entity creations and collect any new queue items.
		newItems := r.processEntityCreations(ctx, host, item.trigger, item.autoResult.EntitiesToCreate, &outcome)
		queue = append(queue, newItems...)
	}

	// Warn if we hit the limit with work remaining.
	if len(queue) > 0 {
		outcome.Warnings = append(outcome.Warnings,
			fmt.Sprintf("automation iteration limit (%d) reached; %d pending items skipped",
				MaxDepth, len(queue)))
	}

	return outcome, nil
}

// processEntityCreations handles entity creation from automation and
// returns new queue items for any newly created entities that have
// their own follow-up automation work.
func (r *Runner) processEntityCreations(
	ctx context.Context,
	host Host,
	trigger *entity.Entity,
	toCreateList []automation.EntityToCreate,
	outcome *Outcome,
) []queueItem {
	var newItems []queueItem

	for _, toCreate := range toCreateList {
		// Tag the write ctx with the originating automation so EVERY write
		// this entry causes is attributed to it (TKT-JJRVX9): the if_exists
		// replace-delete, the entity create, and the trigger relation.
		//
		// Derived per entry, not per cascade run — one run creates entities
		// for several automations — and derived BEFORE handleIfExists, whose
		// replace path deletes the entity being superseded. Deriving it after
		// would leave that delete on the generic `automation` label while the
		// matching create carried the specific one: two labels on adjacent
		// rows of one operation, so a filter on the automation's name would
		// return the create and miss the delete.
		//
		// The cascaded relation-deletes underneath that delete keep
		// `cascade:delete-entity:<id>`, which cascadeHost.DeleteEntity stamps
		// on a ctx it derives itself.
		writeCtx := triggeredByCtx(ctx, toCreate.AutomationName)

		if skip := r.handleIfExists(writeCtx, host, trigger, toCreate, outcome); skip {
			continue
		}

		// Create entity. Runner takes responsibility for the
		// follow-up cascade evaluation on the result; Host.CreateEntity
		// must not fire automations itself (see Host doc).
		created, createErr := host.CreateEntity(writeCtx, toCreate.Type, CreateEntityOptions{
			TemplateVariant: toCreate.Template,
			Properties:      toCreate.Properties,
		})
		if createErr != nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("failed to create automation entity %s: %v", toCreate.Type, createErr))

			continue
		}
		outcome.EntitiesCreated = append(outcome.EntitiesCreated, created)

		// Create relation from trigger if specified.
		if toCreate.RelationFromTrigger != "" {
			r.createTriggerRelation(writeCtx, host, trigger, created, toCreate.RelationFromTrigger, outcome)
		}

		// Run automation on newly created entity.
		newItem := r.runCreatedEntityAutomation(ctx, host, created, outcome)
		if newItem != nil {
			newItems = append(newItems, *newItem)
		}
	}

	return newItems
}

// runCreatedEntityAutomation runs automation on a newly created
// entity and returns a queue item if the result implies more work.
func (r *Runner) runCreatedEntityAutomation(
	ctx context.Context,
	host Host,
	created *entity.Entity,
	outcome *Outcome,
) *queueItem {
	// coverage-ignore-start: defensive: New rejects a nil Engine and the field is unexported, so r.engine is never nil
	// for a Runner obtained via
	// the public constructor
	if r.engine == nil {
		return nil
	}
	// coverage-ignore-end

	newAutoResult := r.engine.Process(ctx, automation.Event{
		Type:   automation.EventEntityCreated,
		Entity: created,
	})

	// Apply property changes from automation.
	if len(newAutoResult.PropertiesSet) > 0 {
		for prop, val := range newAutoResult.PropertiesSet {
			created.SetString(prop, val)
		}
		// Persist the automation's properties onto the stored row.
		if err := host.WriteEntity(ctx, created, newAutoResult.PropertiesSet); err != nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("failed to update automation entity %s: %v", created.ID, err))
		}
	}

	// Return queue item if there's more work to do.
	hasWork := len(newAutoResult.EntitiesToCreate) > 0 || len(newAutoResult.RelationsToCreate) > 0 ||
		len(newAutoResult.LuaToExecute) > 0 ||
		len(newAutoResult.Warnings) > 0 || len(newAutoResult.Errors) > 0
	if hasWork {
		return &queueItem{created, newAutoResult}
	}

	return nil
}

// applyRelationCreations creates relations from automation results.
// Each relation's From is rewritten to the trigger entity's ID before
// validation; the To is looked up to ensure the target exists, and
// the (from-type, type, to-type) tuple is validated against the
// metamodel before persisting.
//
// The write ctx is tagged per entry with the originating automation's name
// (TKT-JJRVX9), the same way executeScriptActions tags scripted actions. It
// must be PER ENTRY, not hoisted out of the loop: one cascade run applies
// relations from several automations, so a single wrap would attribute all of
// them to whichever name happened to be last.
func (r *Runner) applyRelationCreations(
	ctx context.Context,
	host Host,
	triggerEntity *entity.Entity,
	relations []automation.RelationToCreate,
	outcome *Outcome,
) {
	for _, toCreate := range relations {
		// Same shape as processEntityCreations: derive once at the top of the
		// loop BODY. Per entry, never hoisted above the loop — see the doc.
		writeCtx := triggeredByCtx(ctx, toCreate.AutomationName)

		rel := toCreate.Relation
		rel.From = triggerEntity.ID

		targetEntity, err := host.GetEntity(ctx, rel.To)
		if err != nil {
			outcome.Errors = append(outcome.Errors,
				"automation relation target not found: "+rel.To)
			continue
		}
		if err := host.ValidateRelation(rel.Type, triggerEntity.Type, targetEntity.Type); err != nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("automation relation invalid: %v", err))
			continue
		}

		if err := host.WriteRelation(writeCtx, rel); err != nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("failed to create automation relation: %v", err))
			continue
		}
		outcome.RelationsCreated = append(outcome.RelationsCreated, rel)
	}
}

// triggeredByCtx tags ctx with `automation:<name>` so the audit record for the
// resulting write names the automation that caused it rather than the generic
// `automation` label (TKT-JJRVX9).
//
// It declines to tag in two cases, and both are load-bearing.
//
// EMPTY NAME → ctx unchanged, rather than a dangling `automation:`. The
// fallback in entitymanager's recordCascade then supplies the generic
// `automation` label, which is the right answer for a producer with no name to
// give. An automation name is an ordinary optional field on a YAML list entry
// and no loader validation requires it, so "" is reachable, not theoretical.
//
// CTX ALREADY LABELED → ctx unchanged. **The outermost cause wins.**
// `audit.WithTriggeredBy` is an unconditional `context.WithValue`, so tagging
// here would OVERWRITE an enclosing label — and the enclosing one is the more
// useful attribution. The case that matters: a scheduler task
// (`schedule:<task>`) whose write trips an on:created automation. "What did
// last night's task write?" is the single most obvious audit query for a
// scheduler, and answering it requires the cascaded rows to keep the schedule
// label. Naming the inner automation instead would silently shrink that query's
// result set — a forensic regression nothing errors on.
//
// This mirrors recordCascade, which has always stamped its generic label only
// when the ctx label was empty; that `if` is the same policy, and this helper
// now enforces it one layer earlier. `triggered_by` is a single string, not a
// stack, so "both causes" is not representable — see the Known-labels list in
// the audit-log guide.
//
// ALL THREE cascade write paths route through here — relation creations, entity
// creations, and scripted actions. The scripted path used to stamp the label
// inline and unconditionally, which is where both the dangling `automation:`
// and the scheduler-label clobbering came from; keep new paths on this helper
// rather than re-deriving the string.
func triggeredByCtx(ctx context.Context, name string) context.Context {
	if name == "" || audit.TriggeredByFrom(ctx) != "" {
		return ctx
	}
	return audit.WithTriggeredBy(ctx, "automation:"+name)
}

// executeScriptActions dispatches each automation-emitted script
// action to the request's [ScriptRunner]. Failures are appended to
// outcome.Errors and the loop continues — one bad script does not
// abort the cascade. If req.Scripts is nil and any scripted action
// is present, each one is recorded as an error.
func (r *Runner) executeScriptActions(
	ctx context.Context,
	scripts ScriptRunner,
	mutator Mutator,
	newEntity *entity.Entity,
	oldEntity *entity.Entity,
	luaActions []automation.LuaToExecute,
	outcome *Outcome,
) {
	if len(luaActions) == 0 {
		return
	}

	for _, action := range luaActions {
		if action.Code == "" && action.FilePath == "" {
			// Empty action — skip (matches pre-refactor behavior).
			continue
		}
		if scripts == nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("automation %q: no ScriptRunner configured; cannot run scripted action",
					action.AutomationName))
			continue
		}

		actionCtx := triggeredByCtx(ctx, action.AutomationName)
		err := scripts.Run(actionCtx, ScriptAction{
			Code:      action.Code,
			FilePath:  action.FilePath,
			Name:      action.AutomationName,
			NewEntity: newEntity,
			OldEntity: oldEntity,
			// Converted to a plain string: this package is schema-agnostic
			// and may not import metamodel (see ScriptAction.AllowACLBypass).
			AllowACLBypass: string(action.AllowACLBypass),
			Capabilities:   scriptCapsFrom(action),
		}, mutator)
		if err == nil {
			continue
		}

		// Automations have no incoming HTTP request to correlate
		// against, so the slog line uses the automation name +
		// triggering entity id as the natural identity. Operators
		// grep on those rather than a per-request hex.
		triggerID := ""
		if newEntity != nil {
			triggerID = newEntity.ID
		} else if oldEntity != nil {
			triggerID = oldEntity.ID
		}
		slog.Warn("automation script failed",
			"automation", action.AutomationName,
			"entity", triggerID,
			"error", err)
		outcome.Errors = append(outcome.Errors, err.Error())
	}
}

// handleIfExists checks if_exists behavior for entity creation.
// Returns true if the entity creation should be skipped (either
// because an existing target was found and the action says skip /
// error, or because an unknown if_exists value was encountered).
func (r *Runner) handleIfExists(
	ctx context.Context,
	host Host,
	triggerEntity *entity.Entity,
	toCreate automation.EntityToCreate,
	outcome *Outcome,
) bool {
	if toCreate.RelationFromTrigger == "" {
		return false
	}

	existingTarget := host.FindExistingRelationTarget(
		ctx, triggerEntity.ID, toCreate.RelationFromTrigger, toCreate.Type)

	if existingTarget == nil {
		return false
	}

	switch toCreate.IfExists {
	case automation.IfExistsSkip:
		outcome.EntitiesCreated = append(outcome.EntitiesCreated, existingTarget)
		return true
	case automation.IfExistsError:
		outcome.Errors = append(outcome.Errors,
			fmt.Sprintf("entity already exists via %s relation: %s",
				toCreate.RelationFromTrigger, existingTarget.ID))
		return true
	case automation.IfExistsReplace:
		if err := host.DeleteEntity(ctx, existingTarget.Type, existingTarget.ID, true); err != nil {
			outcome.Errors = append(outcome.Errors,
				fmt.Sprintf("failed to delete existing entity for replace: %v", err))
			return true
		}
	default:
		outcome.Errors = append(outcome.Errors,
			fmt.Sprintf("unknown if_exists value %q, skipping entity creation", toCreate.IfExists))
		return true
	}
	return false
}

// createTriggerRelation creates a relation from the trigger entity to
// a newly created entity. Failures are appended to outcome.Errors.
func (r *Runner) createTriggerRelation(
	ctx context.Context,
	host Host,
	triggerEntity, created *entity.Entity,
	relationType string,
	outcome *Outcome,
) {
	if err := host.ValidateRelation(relationType, triggerEntity.Type, created.Type); err != nil {
		outcome.Errors = append(outcome.Errors,
			fmt.Sprintf("automation relation invalid: %v", err))
		return
	}

	rel := entity.NewRelation(triggerEntity.ID, relationType, created.ID)
	if err := host.WriteRelation(ctx, rel); err != nil {
		outcome.Errors = append(outcome.Errors,
			fmt.Sprintf("failed to create automation relation: %v", err))
		return
	}
	outcome.RelationsCreated = append(outcome.RelationsCreated, rel)
}

// scriptCapsFrom reads an action's capability grant through the single
// translation seam (metamodel.Capabilities.Fields, reached via the automation
// accessor so this package needs no metamodel import). Adding a capability
// changes that signature and breaks the build HERE, rather than silently
// dropping the grant on the automation surface only — the failure mode that
// produced the scheduler defect (TKT-YH52OM).
func scriptCapsFrom(a automation.LuaToExecute) ScriptCapabilities {
	http, ai, mail, writeFile, secrets := a.CapabilityFields()
	return ScriptCapabilities{HTTP: http, AI: ai, Mail: mail, WriteFile: writeFile, Secrets: secrets}
}
