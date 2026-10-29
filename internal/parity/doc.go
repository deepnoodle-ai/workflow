// Package parity checks that application code written once compiles
// against both durable host backends (PRD 007, UC-1). It is a module of
// its own so that neither backend module depends on the other. It has no
// code; see parity_test.go.
package parity
