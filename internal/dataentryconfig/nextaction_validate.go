package dataentryconfig

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/metamodel"
)

// validateNextActions validates next_action_bands and next_actions.
//
// The load-bearing checks are reference integrity (a source naming a band or
// an action that does not exist) and the offer union (exactly one member set).
// Both are config-authoring mistakes that would otherwise surface as a
// suggestion silently never firing — the worst failure mode for an advisory
// system, because nothing is visibly broken and the operator concludes the
// feature does not work.
func validateNextActions(cfg *Config, meta *metamodel.Metamodel) []string {
	var errs []string

	bandIDs := make(map[string]bool, len(cfg.NextActionBands))
	for i, b := range cfg.NextActionBands {
		switch {
		case b.ID == "":
			errs = append(errs, fmt.Sprintf(
				"next_action_bands[%d]: id is required", i))
		case bandIDs[b.ID]:
			// Duplicate ids make list-order-is-priority-order ambiguous:
			// which position does a source referencing it get?
			errs = append(errs, fmt.Sprintf(
				"next_action_bands[%d]: duplicate band id %q", i, b.ID))
		default:
			bandIDs[b.ID] = true
		}
		// An unknown prominence is rejected rather than defaulted: silently
		// falling back to `card` would render a band the operator asked to be
		// quiet at full volume, and nothing would say why.
		switch b.Prominence {
		case "", ProminenceBanner, ProminenceNotice, ProminenceStatusBar:
		default:
			errs = append(errs, fmt.Sprintf(
				"next_action_bands[%d]: unknown prominence %q (want %q, %q or %q)",
				i, b.Prominence, ProminenceBanner, ProminenceNotice, ProminenceStatusBar))
		}
	}

	if len(cfg.NextActions) > 0 && len(cfg.NextActionBands) == 0 {
		errs = append(errs, "next_actions: no next_action_bands declared "+
			"(bands are the priority vocabulary; declare at least one)")
	}

	for _, id := range sortedNextActionIDs(cfg.NextActions) {
		errs = append(errs, validateNextActionSource(id, cfg.NextActions[id], cfg, meta, bandIDs)...)
	}
	return errs
}

// sortedNextActionIDs returns source ids in a stable order so validation
// messages do not shuffle between runs on the same config.
func sortedNextActionIDs(m map[string]NextActionSource) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validateNextActionSource(
	id string, s NextActionSource, cfg *Config, meta *metamodel.Metamodel, bandIDs map[string]bool,
) []string {
	var errs []string
	where := fmt.Sprintf("next_actions[%q]", id)

	switch {
	case s.Band == "":
		errs = append(errs, where+": band is required")
	case !bandIDs[s.Band]:
		errs = append(errs, fmt.Sprintf(
			"%s: references unknown band %q%s", where, s.Band, suggestBand(s.Band, cfg)))
	}

	if s.Suggest == "" {
		errs = append(errs, where+": suggest is required (the message shown to the user)")
	}

	errs = append(errs, validateNextActionCandidateSource(where, s, meta)...)

	if s.Pick != "" {
		switch s.Pick {
		case PickStableRandom, PickFirst, PickLeastRecentlyShown:
		default:
			errs = append(errs, fmt.Sprintf(
				"%s: unknown pick %q (want %q, %q or %q)",
				where, s.Pick, PickStableRandom, PickFirst, PickLeastRecentlyShown))
		}
	}

	// An unknown scope is rejected rather than defaulted: silently treating
	// it as entity-scope would give an operator who asked for source-wide
	// deferral the exact nagging they were trying to switch off.
	switch s.DeferScope {
	case "", DeferScopeEntity, DeferScopeSource:
	default:
		errs = append(errs, fmt.Sprintf(
			"%s: unknown defer_scope %q (want %q or %q)",
			where, s.DeferScope, DeferScopeEntity, DeferScopeSource))
	}

	if s.Cooldown != "" {
		if _, err := ParseNextActionDuration(s.Cooldown); err != nil {
			errs = append(errs, fmt.Sprintf(
				"%s: invalid cooldown %q (want a duration like \"3d\", \"12h\")", where, s.Cooldown))
		}
	}

	errs = append(errs, validateNextActionWorlds(where, s, meta)...)

	for i, o := range s.Actions {
		errs = append(errs, validateNextActionOffer(where, i, o, cfg)...)
	}
	return errs
}

// validateNextActionWorlds checks that source_world and every entry in
// visible_worlds names a DECLARED world.
//
// An undeclared name must fail the load rather than silently never matching,
// and the two keys fail that way for different reasons — which is why both are
// checked even though only one of them touches the store:
//
//   - source_world: an unknown world cannot be compiled to a scope, so the
//     source would fall back to querying the default world. That is the exact
//     behavior the key exists to override, and is indistinguishable from never
//     having set it.
//   - visible_worlds: an unknown name simply never equals the world a reader
//     is browsing, so the suggestion silently never appears anywhere. For an
//     advisory surface that is the worst failure mode there is — nothing looks
//     broken and the operator concludes the feature does not work.
//
// Same shape as list.create_world and app.default_world (validate.go). Only
// the NAME is checked: whether the world resolves a face for any type the
// query touches is a per-entity, per-principal question that load time cannot
// answer, and the read gate answers it at request time anyway.
//
// The reserved name "default" is always legal — it names the implicit default
// world, which is not listed in meta.Worlds.
func validateNextActionWorlds(where string, s NextActionSource, meta *metamodel.Metamodel) []string {
	var errs []string
	if err := checkDeclaredWorld(where, "source_world", s.SourceWorld, meta); err != "" {
		errs = append(errs, err)
	}
	for i, w := range s.VisibleWorlds {
		// Indexed so an operator with several entries knows which one.
		if err := checkDeclaredWorld(where, fmt.Sprintf("visible_worlds[%d]", i), w, meta); err != "" {
			errs = append(errs, err)
		}
	}
	return errs
}

// checkDeclaredWorld returns an error message when world is set but not
// declared, or "" when it is fine. Empty and the reserved default name pass.
func checkDeclaredWorld(where, key, world string, meta *metamodel.Metamodel) string {
	if world == "" || world == metamodel.DefaultWorldName {
		return ""
	}
	if meta == nil {
		return fmt.Sprintf("%s: %s is set, but no metamodel is available to validate it against", where, key)
	}
	if _, ok := meta.Worlds[world]; ok {
		return ""
	}
	declared := make([]string, 0, len(meta.Worlds))
	for name := range meta.Worlds {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	return fmt.Sprintf("%s: %s %q is not a declared world (schema.yaml declares: %s)",
		where, key, world, strings.Join(declared, ", "))
}

// validateNextActionCandidateSource checks the three mutually exclusive ways
// a source names its candidates: Query (global), Context (entity-anchored),
// or Count (entity-less aggregate). Exactly one must be set.
func validateNextActionCandidateSource(where string, s NextActionSource, meta *metamodel.Metamodel) []string {
	var errs []string

	var set []string
	if s.Query != "" {
		set = append(set, "query")
	}
	if s.Context != "" {
		set = append(set, "context")
	}
	if s.Count != "" {
		set = append(set, "count")
	}

	switch len(set) {
	case 0:
		errs = append(errs,
			where+": needs one of query (global), context (entity-anchored) or count (whole-graph)")
	case 1:
	default:
		errs = append(errs, fmt.Sprintf(
			"%s: %s are mutually exclusive (a source has one kind of candidate)",
			where, strings.Join(set, " and ")))
	}

	if s.Context != "" && meta != nil {
		if _, ok := meta.GetEntityDef(s.Context); !ok {
			errs = append(errs, fmt.Sprintf(
				"%s: context references unknown entity type %q", where, s.Context))
		}
	}

	if s.Count != "" {
		errs = append(errs, validateNextActionCount(where, s.Count, meta)...)
	}
	// count_ungated only means something alongside count. Silently ignoring
	// it elsewhere would let an operator believe they had opted out of the
	// read gate on a source that never consults it.
	if s.CountUngated && s.Count == "" {
		errs = append(errs, where+": count_ungated only applies to a count source")
	}
	return errs
}

// validateNextActionCount checks the only supported aggregate form,
// "<entity_type> == 0" — the first-run case. Deliberately narrow: a general
// aggregate language is not needed to say "the graph is empty", and every
// operator writing one would be inventing syntax the engine must then honor.
func validateNextActionCount(where, count string, meta *metamodel.Metamodel) []string {
	fields := strings.Fields(count)
	if len(fields) != 3 || fields[1] != "==" || fields[2] != "0" {
		return []string{fmt.Sprintf(
			"%s: count must be of the form \"<entity_type> == 0\", got %q", where, count)}
	}
	if meta != nil {
		if _, ok := meta.GetEntityDef(fields[0]); !ok {
			return []string{fmt.Sprintf(
				"%s: count references unknown entity type %q", where, fields[0])}
		}
	}
	return nil
}

func validateNextActionOffer(where string, i int, o NextActionOffer, cfg *Config) []string {
	var errs []string
	at := fmt.Sprintf("%s: actions[%d]", where, i)

	kinds := o.setKinds()
	switch len(kinds) {
	case 0:
		errs = append(errs,
			at+": empty (set one of action, set, navigate, snooze, dismiss, acknowledge)")
		return errs
	case 1:
	default:
		errs = append(errs, fmt.Sprintf(
			"%s: sets %s — an affordance is exactly one of them", at, strings.Join(kinds, " and ")))
	}

	if o.Action != "" {
		if a, ok := cfg.Actions[o.Action]; !ok {
			errs = append(errs, fmt.Sprintf(
				"%s: references unknown action %q", at, o.Action))
		} else if a.AvailableOn != nil {
			errs = append(errs, entityBoundActionRefError(at, o.Action))
		}
	}

	for _, d := range o.Snooze {
		if _, err := ParseNextActionDuration(d); err != nil {
			errs = append(errs, fmt.Sprintf(
				"%s: invalid snooze duration %q (want e.g. \"1d\", \"7d\", \"12h\")", at, d))
		}
	}

	if o.PickOne != nil {
		errs = append(errs, validatePickOne(at, *o.PickOne, cfg)...)
	}

	// Confirm only means something for an in-place mutation. Silently
	// ignoring it on a navigate/snooze would teach an operator it works.
	if o.Confirm && o.Action == "" && len(o.Set) == 0 {
		errs = append(errs, at+": confirm only applies to action or set")
	}
	return errs
}

// validatePickOne checks a render-time option list.
//
// Both fields are required and neither can be defaulted: without a query
// there are no options, and without an action there is nothing to do with the
// one chosen — either omission produces an affordance that renders but cannot
// work, which is the silent-no-op failure this validation exists to prevent.
func validatePickOne(at string, p NextActionPickOne, cfg *Config) []string {
	var errs []string
	if p.Query == "" {
		errs = append(errs, at+": pick_one needs a query (it is the option list)")
	}
	if p.Action == "" {
		errs = append(errs, at+": pick_one needs an action to run on the chosen option")
	} else if a, ok := cfg.Actions[p.Action]; !ok {
		errs = append(errs, fmt.Sprintf(
			"%s: pick_one references unknown action %q", at, p.Action))
	} else if a.AvailableOn != nil {
		errs = append(errs, entityBoundActionRefError(at+": pick_one", p.Action))
	}
	// A negative limit is a typo, not a request for the default: silently
	// treating it as "3" would hide the mistake.
	if p.Limit < 0 {
		errs = append(errs, fmt.Sprintf(
			"%s: pick_one limit must not be negative, got %d", at, p.Limit))
	}
	if p.Limit > MaxPickOneLimit {
		errs = append(errs, fmt.Sprintf(
			"%s: pick_one limit %d exceeds the maximum of %d (a suggestion offering more "+
				"has stopped being one suggestion)", at, p.Limit, MaxPickOneLimit))
	}
	return errs
}

// ParseNextActionDuration parses a cooldown or snooze duration, extending
// time.ParseDuration with a day unit: "3d" is the natural way to write a
// cooldown, and ParseDuration has no "d". Everything else is delegated
// unchanged, so "12h", "90m" and "1h30m" keep working.
//
// Exported because the engine must parse these the same way the validator
// does — a config that validates but then parses differently at runtime is
// exactly the drift a single function prevents.
// hoursPerDay backs the "d" unit ParseNextActionDuration adds.
const hoursPerDay = 24

func ParseNextActionDuration(s string) (time.Duration, error) {
	if num, ok := strings.CutSuffix(s, "d"); ok && num != "" {
		days, err := strconv.ParseFloat(num, 64)
		if err == nil {
			return time.Duration(days * float64(hoursPerDay*time.Hour)), nil
		}
		// Not a plain number before the "d" (e.g. "1h30d"): fall through and
		// let ParseDuration produce its own error for the whole string.
	}
	return time.ParseDuration(s)
}

// suggestBand offers a near-miss band id for an unknown reference, matching
// the "did you mean" style the view/list validators use.
func suggestBand(want string, cfg *Config) string {
	for _, b := range cfg.NextActionBands {
		if strings.EqualFold(b.ID, want) {
			return fmt.Sprintf(" (did you mean %q?)", b.ID)
		}
	}
	return ""
}
