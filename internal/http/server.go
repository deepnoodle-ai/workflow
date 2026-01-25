// Package http provides HTTP transport for workflow task operations.
package http

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/services"
)

// Server wraps services with HTTP endpoints.
type Server struct {
	handler    http.Handler
	httpServer *http.Server
	logger     *slog.Logger
}

// ServerOptions configures an HTTP Server.
type ServerOptions struct {
	TaskService      *services.TaskService
	ExecutionService *services.ExecutionService
	Auth             Authenticator
	Logger           *slog.Logger
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	// OnTaskCompleted is called after a task is successfully completed.
	// The orchestrator uses this to advance workflow execution.
	OnTaskCompleted  TaskCompletionCallback
}

// NewServer creates a new HTTP server for task and execution operations.
func NewServer(opts ServerOptions) *Server {
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 30 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 30 * time.Second
	}
	if opts.Auth == nil {
		opts.Auth = &NoopAuthenticator{}
	}

	handler := NewHandler(HandlerOptions{
		TaskService:      opts.TaskService,
		ExecutionService: opts.ExecutionService,
		OnTaskCompleted:  opts.OnTaskCompleted,
	})

	mux := http.NewServeMux()

	// Auth middleware wrapper
	authMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			workerID, err := opts.Auth.Authenticate(r)
			if err != nil {
				if authErr, ok := err.(*AuthError); ok {
					http.Error(w, authErr.Message, authErr.StatusCode)
				} else {
					http.Error(w, err.Error(), http.StatusUnauthorized)
				}
				return
			}
			// Add worker ID to context
			ctx := ContextWithWorkerID(r.Context(), workerID)
			next(w, r.WithContext(ctx))
		}
	}

	// Task endpoints (for workers)
	mux.HandleFunc("POST /tasks/claim", authMiddleware(handler.ClaimTask))
	mux.HandleFunc("POST /tasks/{id}/complete", authMiddleware(handler.CompleteTask))
	mux.HandleFunc("POST /tasks/{id}/heartbeat", authMiddleware(handler.HeartbeatTask))
	mux.HandleFunc("POST /tasks/{id}/release", authMiddleware(handler.ReleaseTask))
	mux.HandleFunc("GET /tasks/{id}", authMiddleware(handler.GetTask))

	// Execution endpoints (for clients submitting workflows)
	mux.HandleFunc("POST /executions", authMiddleware(handler.SubmitExecution))
	mux.HandleFunc("GET /executions/{id}", authMiddleware(handler.GetExecution))
	mux.HandleFunc("GET /executions", authMiddleware(handler.ListExecutions))
	mux.HandleFunc("POST /executions/{id}/cancel", authMiddleware(handler.CancelExecution))

	// Health check (no auth)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return &Server{
		handler: mux,
		logger:  opts.Logger,
		httpServer: &http.Server{
			Handler:      mux,
			ReadTimeout:  opts.ReadTimeout,
			WriteTimeout: opts.WriteTimeout,
		},
	}
}

// ListenAndServe starts the server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	s.httpServer.Addr = addr
	if s.logger != nil {
		s.logger.Info("starting HTTP server", "addr", addr)
	}
	return s.httpServer.ListenAndServe()
}

// Serve starts the server on the given listener.
func (s *Server) Serve(l net.Listener) error {
	if s.logger != nil {
		s.logger.Info("starting HTTP server", "addr", l.Addr())
	}
	return s.httpServer.Serve(l)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.logger != nil {
		s.logger.Info("shutting down HTTP server")
	}
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.handler
}
