# Clean Architecture in the Workflow Engine

This document describes the clean architecture principles applied to the workflow engine codebase, with a focus on dependency inversion and package organization.

## Overview

The workflow engine follows clean architecture principles to achieve:

1. **Independence from frameworks** - Business logic doesn't depend on external libraries
2. **Testability** - Core logic can be tested without infrastructure
3. **Independence from UI** - No coupling to presentation concerns
4. **Independence from database** - Storage is an implementation detail
5. **Independence from external agencies** - Business rules don't know about the outside world

## Package Layers

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          PUBLIC API LAYER                                │
│                                                                          │
│  workflow/              - User-facing types and interfaces               │
│  stores/                - Factory functions for concrete implementations │
│                                                                          │
│  Dependencies: domain, internal/engine (for delegation)                  │
│  Does NOT import: database/sql, internal/memory, internal/postgres       │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    │ depends on
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                           DOMAIN LAYER                                   │
│                                                                          │
│  domain/                - Core business types and interfaces             │
│                         - ExecutionRepository, TaskRepository            │
│                         - Store interface (composition of repositories)  │
│                         - SchemaMigrator interface                       │
│                                                                          │
│  Dependencies: Only standard library (context)                           │
└─────────────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ implements
                                    │
┌─────────────────────────────────────────────────────────────────────────┐
│                       IMPLEMENTATION LAYER                               │
│                                                                          │
│  internal/memory/       - In-memory store for testing                    │
│  internal/postgres/     - PostgreSQL store for production                │
│  internal/file/         - File-based checkpointer and logger             │
│  internal/engine/       - Core engine logic                              │
│  internal/services/     - Business logic services                        │
│  internal/http/         - HTTP server and client                         │
│                                                                          │
│  Dependencies: domain, database/sql, os, etc.                            │
└─────────────────────────────────────────────────────────────────────────┘
```

## The Dependency Rule

Dependencies point inward. The domain layer knows nothing about:
- How data is stored (PostgreSQL, memory, files)
- How workflows are executed (HTTP, local process)
- Framework-specific details

```
stores/ ──────────────────┐
                          │
workflow/ ────────────────┼────▶ domain/ ◀──── internal/memory/
                          │                ◀──── internal/postgres/
internal/engine/ ─────────┘                ◀──── internal/file/
```

## Key Interfaces

### Domain Layer Interfaces

The domain package defines pure interfaces with no infrastructure dependencies:

```go
// domain/store.go
package domain

import "context"

// Store composes all repository interfaces
type Store interface {
    ExecutionRepository
    TaskRepository
    EventRepository
    SchemaMigrator
}

// SchemaMigrator is separated to keep persistence concerns explicit
type SchemaMigrator interface {
    CreateSchema(ctx context.Context) error
}
```

### Public API Interfaces

The workflow package re-exports domain types and adds its own abstractions:

```go
// workflow/store.go
package workflow

// ExecutionStore is the public interface for stores
// Note: Does NOT include CreateSchema - use SchemaMigrator separately
type ExecutionStore interface {
    CreateExecution(ctx context.Context, record *ExecutionRecord) error
    GetExecution(ctx context.Context, id string) (*ExecutionRecord, error)
    // ... task operations, recovery operations
}

// SchemaMigrator for stores that need initialization
type SchemaMigrator interface {
    CreateSchema(ctx context.Context) error
}
```

## Dependency Inversion in Practice

### Problem: Infrastructure in Public API

Before refactoring, the public `workflow` package had direct dependencies on infrastructure:

```go
// BAD: workflow/store.go imported infrastructure
import (
    "database/sql"                              // Infrastructure!
    "github.com/deepnoodle-ai/workflow/internal/memory"   // Implementation!
    "github.com/deepnoodle-ai/workflow/internal/postgres" // Implementation!
)

func NewPostgresStore(db *sql.DB) ExecutionStore {
    return postgres.NewStore(...)  // Tight coupling
}
```

This violated dependency inversion because:
1. Users of `workflow` package transitively depended on `database/sql`
2. The public API was coupled to specific implementations
3. Testing required real infrastructure or complex mocking

### Solution: Separate Stores Package

The `stores` package provides factory functions, keeping infrastructure concerns separate:

```go
// stores/stores.go
package stores

import (
    "database/sql"
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/internal/memory"
    "github.com/deepnoodle-ai/workflow/internal/postgres"
)

// Factory functions live here, not in the core workflow package
func NewMemoryStore() workflow.ExecutionStore {
    return workflow.NewStoreAdapter(memory.NewStore())
}

func NewPostgresStore(db *sql.DB, opts ...PostgresStoreOption) workflow.ExecutionStore {
    return workflow.NewStoreAdapter(postgres.NewStore(...))
}

// Helper for schema initialization
func CreateSchema(ctx context.Context, store workflow.ExecutionStore) error {
    if migrator, ok := store.(workflow.SchemaMigrator); ok {
        return migrator.CreateSchema(ctx)
    }
    return nil
}
```

### File-Based Implementations

Similarly, file-based implementations are isolated:

```go
// internal/file/checkpointer.go - Implementation
package file

type Checkpointer struct {
    dataDir string
}

func NewCheckpointer(dataDir string) (*Checkpointer, error) { ... }
func (c *Checkpointer) SaveCheckpoint(...) error { ... }
func (c *Checkpointer) LoadCheckpoint(...) (*workflow.Checkpoint, error) { ... }

// stores/file.go - Factory
package stores

func NewFileCheckpointer(dataDir string) (workflow.Checkpointer, error) {
    return file.NewCheckpointer(dataDir)
}

func NewFileActivityLogger(directory string) workflow.ActivityLogger {
    return file.NewActivityLogger(directory)
}
```

## Import Structure

### Clean Imports in workflow/store.go

```go
package workflow

import (
    "context"
    "time"

    "github.com/deepnoodle-ai/workflow/domain"
    "github.com/deepnoodle-ai/workflow/internal/engine"
)

// NO: database/sql
// NO: internal/memory
// NO: internal/postgres
// NO: os, path/filepath
```

### Clean Imports in domain/store.go

```go
package domain

import "context"

// ONLY standard library
```

## Usage Patterns

### Creating Stores

```go
import (
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/stores"
)

// Testing - in-memory store
store := stores.NewMemoryStore()

// Production - PostgreSQL store
db, _ := sql.Open("postgres", os.Getenv("DATABASE_URL"))
store := stores.NewPostgresStore(db)

// Initialize schema (only for stores that need it)
stores.CreateSchema(ctx, store)

// Or use type assertion
if migrator, ok := store.(workflow.SchemaMigrator); ok {
    migrator.CreateSchema(ctx)
}
```

### Creating File-Based Components

```go
import "github.com/deepnoodle-ai/workflow/stores"

// File checkpointer
checkpointer, err := stores.NewFileCheckpointer("/data/checkpoints")

// File activity logger
logger := stores.NewFileActivityLogger("/data/logs")
```

### In-Memory Components for Testing

```go
import "github.com/deepnoodle-ai/workflow"

// No external dependencies needed
checkpointer := workflow.NewMemoryCheckpointer()
logger := workflow.NewNullActivityLogger()
```

## Benefits

### 1. Clean Public API

The `workflow` package has minimal dependencies:
- No `database/sql`
- No file system operations
- Just domain types and interfaces

### 2. Flexible Testing

Tests can use:
- `workflow.NewMemoryCheckpointer()` for unit tests
- `stores.NewMemoryStore()` for integration tests
- `stores.NewPostgresStore()` only when needed

### 3. Clear Separation of Concerns

| Package | Responsibility | Dependencies |
|---------|---------------|--------------|
| `workflow/` | Public API, interfaces | domain, internal/engine |
| `domain/` | Core types, interfaces | context only |
| `stores/` | Factory functions | workflow, implementations |
| `internal/memory/` | In-memory implementation | domain |
| `internal/postgres/` | PostgreSQL implementation | domain, database/sql |
| `internal/file/` | File implementation | workflow (interfaces) |

### 4. Easier Onboarding

New developers can:
1. Start with `workflow` package for basic usage
2. Import `stores` when they need specific implementations
3. Never need to understand internal packages

## Anti-Patterns to Avoid

### 1. Don't Import Implementations in Domain

```go
// BAD: domain importing implementation
package domain

import "github.com/deepnoodle-ai/workflow/internal/postgres"

type Store interface {
    *postgres.Store  // Don't embed concrete types!
}
```

### 2. Don't Export Internal Types

```go
// BAD: exposing internal types in public API
package workflow

import "github.com/deepnoodle-ai/workflow/internal/memory"

type MemoryStore = memory.Store  // Don't re-export internal types!
```

### 3. Don't Put Factory Functions in Domain

```go
// BAD: factories in domain
package domain

func NewPostgresStore(db *sql.DB) Store {  // Creates infrastructure dependency!
    ...
}
```

## Summary

The clean architecture approach ensures:

1. **Domain types are stable** - Changes to PostgreSQL don't affect domain interfaces
2. **Public API is clean** - Users see only what they need
3. **Implementations are swappable** - Easy to add new store backends
4. **Testing is straightforward** - In-memory implementations for tests
5. **Dependencies flow inward** - Infrastructure depends on domain, never reverse
