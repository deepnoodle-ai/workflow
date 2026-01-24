package http

import (
	"context"
	"net/http"
	"strings"
)

// Authenticator validates requests and extracts identity information.
type Authenticator interface {
	// Authenticate validates the request and returns the worker ID if valid.
	// Returns an error if authentication fails.
	Authenticate(r *http.Request) (workerID string, err error)
}

// NoopAuthenticator allows all requests and extracts worker ID from header.
type NoopAuthenticator struct{}

// Authenticate extracts the worker ID from the X-Worker-ID header.
func (a *NoopAuthenticator) Authenticate(r *http.Request) (string, error) {
	return r.Header.Get("X-Worker-ID"), nil
}

// TokenAuthenticator validates Bearer tokens.
type TokenAuthenticator struct {
	// ValidTokens is a set of valid tokens.
	ValidTokens map[string]bool
}

// NewTokenAuthenticator creates a token-based authenticator.
func NewTokenAuthenticator(tokens []string) *TokenAuthenticator {
	valid := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		valid[t] = true
	}
	return &TokenAuthenticator{ValidTokens: valid}
}

// Authenticate validates the Bearer token and extracts worker ID from header.
func (a *TokenAuthenticator) Authenticate(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", &AuthError{Message: "Authorization header required", StatusCode: http.StatusUnauthorized}
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", &AuthError{Message: "Invalid authorization format", StatusCode: http.StatusUnauthorized}
	}

	token := parts[1]
	if !a.ValidTokens[token] {
		return "", &AuthError{Message: "Invalid token", StatusCode: http.StatusUnauthorized}
	}

	return r.Header.Get("X-Worker-ID"), nil
}

// AuthError represents an authentication error.
type AuthError struct {
	Message    string
	StatusCode int
}

func (e *AuthError) Error() string {
	return e.Message
}

// workerIDKey is the context key for worker ID.
type workerIDKey struct{}

// WorkerIDFromContext extracts the worker ID from the context.
func WorkerIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(workerIDKey{}).(string); ok {
		return id
	}
	return ""
}

// ContextWithWorkerID adds the worker ID to the context.
func ContextWithWorkerID(ctx context.Context, workerID string) context.Context {
	return context.WithValue(ctx, workerIDKey{}, workerID)
}
