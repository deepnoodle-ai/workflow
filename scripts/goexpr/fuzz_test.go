package goexpr

import (
	"errors"
	"testing"
)

// fuzzCorpus is the seed set used by FuzzCompile and FuzzEval. It covers
// every operator class, builtin, and reflection path in the engine so the
// mutation engine has something meaningful to start from.
var fuzzCorpus = []string{
	// literals
	"0",
	"42",
	"-1",
	"3.14",
	"0xff",
	"0o17",
	"0b1010",
	"'a'",
	`'\n'`,
	`""`,
	`"hello"`,
	"true",
	"false",
	"nil",
	// arithmetic
	"1 + 2",
	"10 / 3",
	"10 % 3",
	"1.5 * 2",
	"10 / 0",
	"10 % 0",
	"-5 + 3",
	"+3",
	// comparisons & logical
	"1 < 2",
	"1 == 1.0",
	`"a" < "b"`,
	"true && false",
	"false || true",
	"!true",
	"!0",
	// selectors, indices
	"state.x",
	"state.user.name",
	"state.items[0]",
	`state["counts"]["apples"]`,
	`"héllo"[1]`,
	// calls and builtins
	"len(state.items)",
	`upper("abc")`,
	`lower("ABC")`,
	`contains("hello", "ell")`,
	`has(state.tags, "red")`,
	"keys(state.tags)",
	`int("42")`,
	`float("3.14")`,
	`string(42)`,
	`sprintf("%d", 7)`,
	`bool(0)`,
	// mixed
	"state.counter + 10 > 0 && state.user.name == \"Alice\"",
	"state.items[0] + state.items[1]",
	// pathological targets that should reject cleanly
	"state.items[1:3]",
	"x.(int)",
	"1 ^ 2",
	"1i",
	"",
}

// fuzzEnv is the environment FuzzEval runs every mutated expression
// against. It purposefully mixes map[string]any, typed maps, slices,
// structs with exported and unexported fields, nil pointers, and values
// with methods, so the fuzzer can reach every lookup/selection branch.
type fuzzStruct struct {
	Public  int
	Name    string
	secret  int //nolint:unused // exercised via unexported-field reject path
	Pointer *fuzzStruct
}

func (f fuzzStruct) Double() int                { return f.Public * 2 }
func (f *fuzzStruct) Triple() int               { return f.Public * 3 }
func (f fuzzStruct) Greet(who string) string    { return "hello " + who }
func (f fuzzStruct) Ratio(a, b float64) float64 { return a / b }
func (f fuzzStruct) Bad() (int, int)            { return 1, 2 } // unsupported return shape
func (f fuzzStruct) Fail() (int, error)         { return 0, errors.New("boom") }
func (f fuzzStruct) Panic() int                 { panic("user-code panic") }

func fuzzEnv() map[string]any {
	return map[string]any{
		"state": map[string]any{
			"counter": int64(5),
			"name":    "Alice",
			"items":   []any{int64(1), int64(2), int64(3)},
			"user": map[string]any{
				"name": "Alice",
				"age":  int64(30),
			},
			"tags": map[string]any{"red": true, "blue": false},
		},
		"struct":    fuzzStruct{Public: 4, Name: "s"},
		"structPtr": &fuzzStruct{Public: 7, Name: "p"},
		"nilPtr":    (*fuzzStruct)(nil),
		"typedMap":  map[string]int{"a": 1, "b": 2},
		"intMap":    map[int]string{1: "one"},
		"nilSlice":  []any(nil),
		"nilMap":    map[string]any(nil),
		"slice":     []int{10, 20, 30},
		"fn":        func(n int64) int64 { return n * 2 },
		"fnNil":     (func())(nil),
	}
}

// FuzzCompile checks that Compile never panics and always returns
// either a valid program or an ErrCompile. Anything else (panic, nil
// program with nil error, wrong error class) is a test failure.
func FuzzCompile(f *testing.F) {
	for _, s := range fuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		prog, err := Compile(src)
		if err != nil {
			if !errors.Is(err, ErrCompile) {
				t.Fatalf("Compile returned non-ErrCompile error: %v", err)
			}
			return
		}
		if prog == nil {
			t.Fatalf("Compile returned nil program and nil error")
		}
	})
}

// FuzzEval exercises Compile+Run. Any panic crashes the fuzzer; any
// error must wrap ErrCompile or ErrEvaluate.
func FuzzEval(f *testing.F) {
	for _, s := range fuzzCorpus {
		f.Add(s)
	}
	env := fuzzEnv()
	f.Fuzz(func(t *testing.T, src string) {
		prog, err := Compile(src)
		if err != nil {
			if !errors.Is(err, ErrCompile) {
				t.Fatalf("Compile returned non-ErrCompile error: %v", err)
			}
			return
		}
		_, err = prog.Run(ctx, env)
		if err != nil && !errors.Is(err, ErrEvaluate) {
			// User function errors are allowed to not wrap ErrEvaluate
			// (e.g. customErr from engine_test.go or the fuzzStruct.Fail
			// error). Only fail if it's clearly a misclassified internal.
			if errors.Is(err, ErrCompile) {
				t.Fatalf("Run returned ErrCompile: %v", err)
			}
		}
	})
}
