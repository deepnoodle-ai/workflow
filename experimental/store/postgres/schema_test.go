package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"empty", "", true},
		{"simple", "public", false},
		{"underscore_prefix", "_priv", false},
		{"letters_digits", "tenant_42", false},
		{"starts_with_digit", "1tenant", true},
		{"contains_dash", "my-schema", true},
		{"contains_dot", "a.b", true},
		{"contains_quote", `bad"name`, true},
		{"contains_space", "my schema", true},
		{"contains_semicolon", "a;b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateIdentifier(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
		})
	}
}

func TestWithSchemaPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid schema name")
		}
	}()
	// Passing a non-nil-but-unusable pool value would require a real
	// pool; instead we validate that New + WithSchema with a bad
	// name panics before reaching any pool call.
	// We only need a non-nil *pgxpool.Pool pointer; New panics on
	// schema validation before touching the pool.
	New(&pgxpool.Pool{}, WithSchema("bad-name"))
}

func TestNewDefaultsToPublicSchema(t *testing.T) {
	s := New(&pgxpool.Pool{})
	if s.Schema() != "public" {
		t.Fatalf("default schema = %q, want %q", s.Schema(), "public")
	}
}

func TestWithSchemaCustom(t *testing.T) {
	s := New(&pgxpool.Pool{}, WithSchema("tenant_42"))
	if s.Schema() != "tenant_42" {
		t.Fatalf("schema = %q, want %q", s.Schema(), "tenant_42")
	}
	got := s.t("workflow_runs")
	want := `"tenant_42"."workflow_runs"`
	if got != want {
		t.Fatalf("t() = %q, want %q", got, want)
	}
}
