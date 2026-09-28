package appbuild

import (
	"context"
	"errors"
	"reflect"

	"github.com/Sourcehaven-BV/rela/internal/entity"
	"github.com/Sourcehaven-BV/rela/internal/entitymanager"
)

// aliasFanout dispatches one identity-change notification to several
// subscribers.
//
// [entitymanager.Deps.AliasRewriter] is a single field because it was
// introduced for a single consumer (the CalDAV alias service). More than one
// subsystem now holds references BY ENTITY ID — comments are keyed on the
// target's id too — and both must learn about a rename. Fanning out here keeps
// that a wiring concern: entitymanager still depends on the two methods it
// calls, and does not grow a registry.
//
// Errors from every subscriber are joined rather than short-circuited, so one
// failing subscriber cannot mask another's failure or stop it being notified.
// The Manager logs what it gets back; see the AliasRewriter doc for why a
// post-write hook cannot fail the write that already happened.
type aliasFanout struct {
	subscribers []entitymanager.AliasRewriter
}

var _ entitymanager.AliasRewriter = (*aliasFanout)(nil)

// newAliasFanout returns a rewriter over the non-nil subscribers.
//
// Nil: returns nil when nothing subscribes, so the Manager's `if
// AliasRewriter == nil` fast path still applies and an unused hook costs
// nothing. Returns the single subscriber unwrapped when there is exactly one,
// keeping the common case free of an indirection.
//
// A disabled subsystem reaches us as a typed nil — buildComments and
// buildStateAndAliases both signal "feature off" by returning a nil
// *comments.Service / *caldavalias.Service, and passing that concrete pointer
// into this variadic boxes it into an interface with a non-nil type word. A
// plain `s != nil` does not see through that box, so the dead subscriber was
// kept and the first delete dereferenced it. Hence isNilSubscriber.
//
// This settles the question for alias subscribers only. The same two services
// also travel to [Services] as concrete fields, where `!= nil` does work and
// each consumer nil-checks them separately; nothing here protects those.
func newAliasFanout(subs ...entitymanager.AliasRewriter) entitymanager.AliasRewriter {
	live := make([]entitymanager.AliasRewriter, 0, len(subs))
	for _, s := range subs {
		if !isNilSubscriber(s) {
			live = append(live, s)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	default:
		return &aliasFanout{subscribers: live}
	}
}

// isNilSubscriber reports whether s is nil, including a nil pointer boxed into
// a non-nil interface.
//
// Nil: accepted — answering "is this nil" for both nil shapes is the whole
// point, so a nil argument is an expected input rather than a caller error.
//
// Reflection rather than a type switch over the known subscribers: the two
// current typed-nil sources are not the point, the wiring pattern is. Any
// later "disabled subsystem returns a nil *T" would reintroduce the same
// crash, and a type switch would silently not cover it.
func isNilSubscriber(s entitymanager.AliasRewriter) bool {
	if s == nil {
		return true
	}
	switch v := reflect.ValueOf(s); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// EntityRenamed notifies every subscriber that an entity's id changed.
func (f *aliasFanout) EntityRenamed(ctx context.Context, oldID, newID string) error {
	var errs []error
	for _, s := range f.subscribers {
		if err := s.EntityRenamed(ctx, oldID, newID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EntityDeleted notifies every subscriber that an entity left the graph.
func (f *aliasFanout) EntityDeleted(ctx context.Context, entityID string) error {
	var errs []error
	for _, s := range f.subscribers {
		if err := s.EntityDeleted(ctx, entityID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// EntityFaceDeleted notifies every subscriber that one face of an entity left
// the graph.
func (f *aliasFanout) EntityFaceDeleted(ctx context.Context, entityID string, face entity.Face) error {
	var errs []error
	for _, s := range f.subscribers {
		if err := s.EntityFaceDeleted(ctx, entityID, face); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
