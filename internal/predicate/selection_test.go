package predicate_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Sourcehaven-BV/rela/internal/predicate"
)

// selectionEnv declares an entity with string, int, bool, list and
// custom-layout date properties.
func selectionEnv(t *testing.T) *predicate.Env {
	t.Helper()
	env := predicate.NewEnv()
	if err := env.DeclareVar("entity", predicate.RecordType{
		"method": predicate.StringType,
		"opt":    predicate.StringType,
		"score":  predicate.IntType,
		"flag":   predicate.BoolType,
		"tags":   predicate.ListType{Elem: predicate.StringType},
		"due":    predicate.DateTypeWithLayout("02-01-2006"),
		"amount": predicate.NumberType,
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.DeclareFunc("host_check", predicate.FuncSig{Return: predicate.BoolType}); err != nil {
		t.Fatal(err)
	}
	return env
}

func evalWith(t *testing.T, prog *predicate.Program, fields map[string]predicate.Value) predicate.Value {
	t.Helper()
	b := predicate.NewBindings()
	if err := b.SetVar("entity", predicate.NewRecord(fields)); err != nil {
		t.Fatal(err)
	}
	v, err := prog.Eval(context.Background(), b)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	return v
}

func TestSelection_EnumMapping(t *testing.T) {
	const src = `(entity.method == 'password_otp' and 'medium')
		or ((entity.method == 'passkey' or entity.method == 'hardware_key') and 'high')
		or 'low'`
	prog, err := predicate.CompileValue(selectionEnv(t), src, predicate.ValueProfile(predicate.StringType))
	if err != nil {
		t.Fatalf("CompileValue: %v", err)
	}
	if !prog.SQLPortable() {
		t.Error("selection over properties and literals should be SQL-portable")
	}
	tests := []struct {
		name   string
		fields map[string]predicate.Value
		want   string
	}{
		{"otp", map[string]predicate.Value{"method": predicate.NewString("password_otp")}, "medium"},
		{"passkey", map[string]predicate.Value{"method": predicate.NewString("passkey")}, "high"},
		{"hardware", map[string]predicate.Value{"method": predicate.NewString("hardware_key")}, "high"},
		{"password", map[string]predicate.Value{"method": predicate.NewString("password")}, "low"},
		{"missing", map[string]predicate.Value{}, "low"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := evalWith(t, prog, tc.fields); got != predicate.NewString(tc.want) {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}

func TestSelection_LuaSemantics(t *testing.T) {
	env := selectionEnv(t)
	str := predicate.ValueProfile(predicate.StringType)
	tests := []struct {
		name   string
		src    string
		fields map[string]predicate.Value
		want   predicate.Value
	}{
		{"default when absent", "entity.opt or 'none'", map[string]predicate.Value{}, predicate.NewString("none")},
		{"value when set", "entity.opt or 'none'",
			map[string]predicate.Value{"opt": predicate.NewString("x")}, predicate.NewString("x")},
		{"empty string is a value", "entity.opt or 'none'",
			map[string]predicate.Value{"opt": predicate.NewString("")}, predicate.NewString("")},
		{"false condition yields nil", "entity.score > 5 and 'big'",
			map[string]predicate.Value{"score": predicate.NewInt(1)}, predicate.NewNil()},
		// Lua semantics: a nil chosen value falls through to the next
		// alternative even though the condition is true.
		{"nil branch falls through", "entity.score > 5 and entity.opt or 'low'",
			map[string]predicate.Value{"score": predicate.NewInt(9)}, predicate.NewString("low")},
		{"guarded nil branch", "entity.score > 5 and (entity.opt or 'none') or 'low'",
			map[string]predicate.Value{"score": predicate.NewInt(9)}, predicate.NewString("none")},
		{"chain without parentheses", "entity.score > 5 and 'a' or entity.score > 2 and 'b' or 'c'",
			map[string]predicate.Value{"score": predicate.NewInt(3)}, predicate.NewString("b")},
		{"inside concatenation", "'level: ' .. (entity.flag and 'on' or 'off')",
			map[string]predicate.Value{"flag": predicate.NewBool(true)}, predicate.NewString("level: on")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := predicate.CompileValue(env, tc.src, str)
			if err != nil {
				t.Fatalf("CompileValue: %v", err)
			}
			if got := evalWith(t, prog, tc.fields); got != tc.want {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSelection_LiteralCoercion(t *testing.T) {
	env := selectionEnv(t)
	intProfile := predicate.ValueProfile(predicate.IntType)
	for _, src := range []string{
		"entity.score > 5 and 10 or 0",
		"entity.score > 5 and 10 or entity.score",
		"(entity.score > 5 and 10 or 0) + entity.score",
	} {
		t.Run(src, func(t *testing.T) {
			prog, err := predicate.CompileValue(env, src, intProfile)
			if err != nil {
				t.Fatalf("CompileValue: %v", err)
			}
			if _, ok := evalWith(t, prog, map[string]predicate.Value{"score": predicate.NewInt(7)}).(predicate.Int); !ok {
				t.Fatal("result is not an int")
			}
		})
	}

	t.Run("bare literal root", func(t *testing.T) {
		prog, err := predicate.CompileValue(env, "5", intProfile)
		if err != nil {
			t.Fatalf("CompileValue: %v", err)
		}
		if got := evalWith(t, prog, nil); got != predicate.NewInt(5) {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("under comparison", func(t *testing.T) {
		prog, err := predicate.Compile(env, "(entity.flag and 10 or 0) == entity.score")
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		got := evalWith(t, prog, map[string]predicate.Value{
			"flag": predicate.NewBool(true), "score": predicate.NewInt(10),
		})
		if got != predicate.NewBool(true) {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("date uses the property layout", func(t *testing.T) {
		prog, err := predicate.CompileValue(env, "entity.flag and '31-12-2026' or entity.due",
			predicate.ValueProfile(predicate.DateTypeWithLayout("02-01-2006")))
		if err != nil {
			t.Fatalf("CompileValue: %v", err)
		}
		got, ok := evalWith(t, prog, map[string]predicate.Value{"flag": predicate.NewBool(true)}).(predicate.Date)
		if !ok || !got.Time().Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("date literal under or uses the sibling's layout", func(t *testing.T) {
		prog, err := predicate.CompileValue(env, "entity.due or '31-12-2026'",
			predicate.ValueProfile(predicate.DateType))
		if err != nil {
			t.Fatalf("CompileValue: %v", err)
		}
		got, ok := evalWith(t, prog, map[string]predicate.Value{}).(predicate.Date)
		if !ok || !got.Time().Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("fractional literal for int", func(t *testing.T) {
		_, err := predicate.CompileValue(env, "entity.flag and 1.5 or entity.score", intProfile)
		if err == nil {
			t.Fatal("want error for a fractional int literal")
		}
	})
}

func TestSelection_CompileErrors(t *testing.T) {
	env := selectionEnv(t)
	tests := []struct {
		name, src, want string
		profile         predicate.Profile
	}{
		{"mixed branch types", "entity.flag and 'x' or 1", "same type", predicate.ValueProfile(predicate.StringType)},
		{"value condition", "entity.method and 'x' or 'y'", "requires bool on left", predicate.ValueProfile(predicate.StringType)},
		{"bool or value", "entity.flag or 'x'", "mixes bool and value", predicate.ValueProfile(predicate.StringType)},
		{"bool and bool-valued select", "entity.score > 1 and 'x' or entity.flag", "mixes bool and value", predicate.ValueProfile(predicate.StringType)},
		{"nil branch", "entity.flag and nil or 'y'", "cannot select nil", predicate.ValueProfile(predicate.StringType)},
		{"list branch", "entity.flag and entity.tags", "got list", predicate.ValueProfile(predicate.StringType)},
		{"record branch", "entity.flag and entity", "got record", predicate.ValueProfile(predicate.StringType)},
		{"wrong result type", "entity.flag and 'x' or 'y'", "top-level expression must be int", predicate.ValueProfile(predicate.IntType)},
		{"select as condition", "entity.flag and 'x' or 'y'", "top-level expression must be bool", predicate.Profile{Expected: predicate.BoolType}},
		{"number and int values", "entity.amount or entity.score", "same type", predicate.ValueProfile(predicate.IntType)},
		{"fractional literal under or", "entity.score or 1.5", "1.5 cannot be an int", predicate.ValueProfile(predicate.IntType)},
		{"bad date literal at the root", "entity.flag and 'nope' or '01-01-2026'", "error at line 1: invalid date", predicate.ValueProfile(predicate.DateTypeWithLayout("02-01-2006"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := predicate.CompileValue(env, tc.src, tc.profile)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSelection_IfHintNamesWorkingForm(t *testing.T) {
	_, err := predicate.CompileValue(selectionEnv(t), "if entity.flag then return 'a' end",
		predicate.ValueProfile(predicate.StringType))
	if err == nil || !strings.Contains(err.Error(), "c and x or y") {
		t.Fatalf("err = %v", err)
	}
	prog, err := predicate.CompileValue(selectionEnv(t), "entity.flag and 'a' or 'b'",
		predicate.ValueProfile(predicate.StringType))
	if err != nil || prog == nil {
		t.Fatalf("the hinted form must compile: %v", err)
	}
}

func TestSelection_DependenciesInsideBranches(t *testing.T) {
	prog, err := predicate.CompileValue(selectionEnv(t), "entity.flag and (host_check() and 'x' or entity.opt) or 'y'",
		predicate.ValueProfile(predicate.StringType))
	if err != nil {
		t.Fatalf("CompileValue: %v", err)
	}
	if got, want := prog.Attributes("entity"), []string{"flag", "opt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attributes = %v, want %v", got, want)
	}
	if got := prog.Functions(); !reflect.DeepEqual(got, []string{"host_check"}) {
		t.Fatalf("functions = %v", got)
	}
}

// A nil condition is an eval error, as in boolean logic, not Lua's falsy
// nil; comparing with true is the documented workaround.
func TestSelection_NilConditionIsAnError(t *testing.T) {
	env := selectionEnv(t)
	str := predicate.ValueProfile(predicate.StringType)
	prog, err := predicate.CompileValue(env, "entity.flag and 'on' or 'off'", str)
	if err != nil {
		t.Fatalf("CompileValue: %v", err)
	}
	b := predicate.NewBindings()
	_ = b.SetVar("entity", predicate.NewRecord(map[string]predicate.Value{}))
	if _, evalErr := prog.Eval(context.Background(), b); evalErr == nil ||
		!strings.Contains(evalErr.Error(), "expected bool, got nil") {

		t.Fatalf("err = %v, want a nil-condition error", evalErr)
	}
	guarded, err := predicate.CompileValue(env, "entity.flag == true and 'on' or 'off'", str)
	if err != nil {
		t.Fatalf("CompileValue: %v", err)
	}
	if got := evalWith(t, guarded, map[string]predicate.Value{}); got != predicate.NewString("off") {
		t.Fatalf("guarded = %#v, want off", got)
	}
}
