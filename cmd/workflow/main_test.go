package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestShellParamsOverrideEnvironment(t *testing.T) {
	t.Setenv("HOME", "/real/home")
	t.Setenv("KEEP", "inherited")
	h := newHandlers(&bytes.Buffer{})["shell"]
	out, err := h(context.Background(), workflow.Job{Command: workflow.Command{
		Config: map[string]any{"shell": `echo "$HOME $KEEP"`},
		Params: map[string]any{"home": "/from-param"},
	}})
	require.NoError(t, err)
	require.Equal(t, "/from-param inherited", out)
}

func TestHandlersKeepStdoutForTheResult(t *testing.T) {
	var out bytes.Buffer
	hs := newHandlers(&out)
	_, err := hs["print"](context.Background(), workflow.Job{Command: workflow.Command{Params: map[string]any{"message": "hello"}}})
	require.NoError(t, err)
	v, err := hs["shell"](context.Background(), workflow.Job{Command: workflow.Command{
		Config: map[string]any{"shell": "echo result; echo noise >&2"},
	}})
	require.NoError(t, err)
	require.Equal(t, "result", v)
	require.Equal(t, "hello\nnoise\n", out.String())
}
