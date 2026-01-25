package engine

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/domain"
)

func TestLooksLikeTimeout(t *testing.T) {
	tests := []struct {
		errMsg   string
		expected bool
	}{
		{"connection timeout", true},
		{"TIMEOUT occurred", true},
		{"context deadline exceeded", true},
		{"context canceled", true},
		{"Deadline Exceeded", true},
		{"service is temporarily unavailable", false},
		{"connection refused", false},
		{"kaboom", false},
		{"", false},
	}

	for _, tc := range tests {
		result := looksLikeTimeout(tc.errMsg)
		if result != tc.expected {
			t.Errorf("looksLikeTimeout(%q) = %v, want %v", tc.errMsg, result, tc.expected)
		}
	}
}

func TestFindMatchingRetryConfig_EmptyErrorEquals(t *testing.T) {
	configs := []*domain.RetryConfig{
		{MaxRetries: 3}, // Empty ErrorEquals matches all
	}

	result := findMatchingRetryConfig("any error", configs)
	if result == nil {
		t.Error("expected match with empty ErrorEquals")
	}
}

func TestFindMatchingRetryConfig_AllErrorType(t *testing.T) {
	configs := []*domain.RetryConfig{
		{ErrorEquals: []string{"all"}, MaxRetries: 3},
	}

	tests := []string{
		"timeout error",
		"service unavailable",
		"kaboom",
		"",
	}

	for _, errMsg := range tests {
		result := findMatchingRetryConfig(errMsg, configs)
		if result == nil {
			t.Errorf("expected 'all' to match error: %q", errMsg)
		}
	}

	// Also test uppercase ALL and wildcard
	for _, pattern := range []string{"ALL", "*"} {
		configs := []*domain.RetryConfig{
			{ErrorEquals: []string{pattern}, MaxRetries: 3},
		}
		result := findMatchingRetryConfig("some error", configs)
		if result == nil {
			t.Errorf("expected %q to match any error", pattern)
		}
	}
}

func TestFindMatchingRetryConfig_ActivityFailed(t *testing.T) {
	configs := []*domain.RetryConfig{
		{ErrorEquals: []string{"activity_failed"}, MaxRetries: 3},
	}

	// Should match non-timeout errors
	nonTimeoutErrors := []string{
		"service unavailable",
		"connection refused",
		"kaboom",
		"internal error",
	}

	for _, errMsg := range nonTimeoutErrors {
		result := findMatchingRetryConfig(errMsg, configs)
		if result == nil {
			t.Errorf("expected activity_failed to match non-timeout error: %q", errMsg)
		}
	}

	// Should NOT match timeout errors
	timeoutErrors := []string{
		"connection timeout",
		"context deadline exceeded",
		"context canceled",
	}

	for _, errMsg := range timeoutErrors {
		result := findMatchingRetryConfig(errMsg, configs)
		if result != nil {
			t.Errorf("expected activity_failed to NOT match timeout error: %q", errMsg)
		}
	}
}

func TestFindMatchingRetryConfig_Timeout(t *testing.T) {
	configs := []*domain.RetryConfig{
		{ErrorEquals: []string{"timeout"}, MaxRetries: 3},
	}

	// Should match timeout errors
	timeoutErrors := []string{
		"connection timeout",
		"context deadline exceeded",
		"context canceled",
		"request TIMEOUT",
	}

	for _, errMsg := range timeoutErrors {
		result := findMatchingRetryConfig(errMsg, configs)
		if result == nil {
			t.Errorf("expected timeout to match: %q", errMsg)
		}
	}

	// Should NOT match non-timeout errors
	nonTimeoutErrors := []string{
		"service unavailable",
		"kaboom",
	}

	for _, errMsg := range nonTimeoutErrors {
		result := findMatchingRetryConfig(errMsg, configs)
		if result != nil {
			t.Errorf("expected timeout to NOT match: %q", errMsg)
		}
	}
}

func TestFindMatchingRetryConfig_CustomErrorType(t *testing.T) {
	configs := []*domain.RetryConfig{
		{ErrorEquals: []string{"network", "database"}, MaxRetries: 3},
	}

	// Should match via substring
	matchingErrors := []string{
		"network connection failed",
		"database connection lost",
		"NETWORK error",
		"DATABASE unavailable",
	}

	for _, errMsg := range matchingErrors {
		result := findMatchingRetryConfig(errMsg, configs)
		if result == nil {
			t.Errorf("expected custom type to match: %q", errMsg)
		}
	}

	// Should NOT match unrelated errors
	result := findMatchingRetryConfig("kaboom", configs)
	if result != nil {
		t.Error("expected custom type to NOT match unrelated error")
	}
}

func TestFindMatchingRetryConfig_NoConfigs(t *testing.T) {
	result := findMatchingRetryConfig("any error", nil)
	if result != nil {
		t.Error("expected nil for empty config list")
	}

	result = findMatchingRetryConfig("any error", []*domain.RetryConfig{})
	if result != nil {
		t.Error("expected nil for empty config list")
	}
}

func TestFindMatchingRetryConfig_MultipleConfigs(t *testing.T) {
	configs := []*domain.RetryConfig{
		{ErrorEquals: []string{"timeout"}, MaxRetries: 1},
		{ErrorEquals: []string{"activity_failed"}, MaxRetries: 5},
	}

	// Timeout should match first config
	result := findMatchingRetryConfig("connection timeout", configs)
	if result == nil || result.MaxRetries != 1 {
		t.Error("expected timeout to match first config")
	}

	// Non-timeout should match second config
	result = findMatchingRetryConfig("kaboom", configs)
	if result == nil || result.MaxRetries != 5 {
		t.Error("expected non-timeout to match second config (activity_failed)")
	}
}

func TestFindMatchingCatchConfig_AllErrorType(t *testing.T) {
	configs := []*domain.CatchConfig{
		{ErrorEquals: []string{"all"}, Next: "error-handler"},
	}

	tests := []string{
		"timeout error",
		"service unavailable",
		"kaboom",
	}

	for _, errMsg := range tests {
		result := findMatchingCatchConfig(errMsg, configs)
		if result == nil {
			t.Errorf("expected 'all' to match error: %q", errMsg)
		}
		if result.Next != "error-handler" {
			t.Errorf("expected Next='error-handler', got %q", result.Next)
		}
	}
}

func TestFindMatchingCatchConfig_ActivityFailed(t *testing.T) {
	configs := []*domain.CatchConfig{
		{ErrorEquals: []string{"activity_failed"}, Next: "recovery"},
	}

	// Should match non-timeout errors
	result := findMatchingCatchConfig("kaboom", configs)
	if result == nil {
		t.Error("expected activity_failed to match non-timeout error")
	}

	// Should NOT match timeout errors
	result = findMatchingCatchConfig("connection timeout", configs)
	if result != nil {
		t.Error("expected activity_failed to NOT match timeout error")
	}
}

func TestFindMatchingCatchConfig_EmptyErrorEquals(t *testing.T) {
	configs := []*domain.CatchConfig{
		{Next: "catch-all"}, // Empty ErrorEquals matches all
	}

	result := findMatchingCatchConfig("any error", configs)
	if result == nil {
		t.Error("expected match with empty ErrorEquals")
	}
	if result.Next != "catch-all" {
		t.Errorf("expected Next='catch-all', got %q", result.Next)
	}
}

func TestFindMatchingCatchConfig_NoMatch(t *testing.T) {
	configs := []*domain.CatchConfig{
		{ErrorEquals: []string{"timeout"}, Next: "timeout-handler"},
	}

	// Non-timeout should not match
	result := findMatchingCatchConfig("kaboom", configs)
	if result != nil {
		t.Error("expected no match for non-timeout error")
	}
}
