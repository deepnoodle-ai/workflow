package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/deepnoodle-ai/wonton/color"
)

// NewLogger returns a logger that writes to stdout with colorized output if
// stdout is a terminal.
func NewLogger() *slog.Logger {
	return slog.New(NewColorHandler(os.Stdout, &ColorHandlerOptions{
		Level:      slog.LevelInfo,
		TimeFormat: time.RFC3339,
	}))
}

// NewJSONLogger returns a logger that writes to stdout in JSON format.
func NewJSONLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// ColorHandlerOptions configures the ColorHandler.
type ColorHandlerOptions struct {
	Level      slog.Level
	TimeFormat string
	NoColor    bool
}

// ColorHandler is a slog.Handler that outputs colorized log messages.
type ColorHandler struct {
	out        io.Writer
	level      slog.Level
	timeFormat string
	noColor    bool
	attrs      []slog.Attr
	groups     []string
	mu         *sync.Mutex
}

// NewColorHandler creates a new ColorHandler.
func NewColorHandler(out io.Writer, opts *ColorHandlerOptions) *ColorHandler {
	h := &ColorHandler{
		out:        out,
		level:      slog.LevelInfo,
		timeFormat: time.RFC3339,
		mu:         &sync.Mutex{},
	}

	if opts != nil {
		h.level = opts.Level
		if opts.TimeFormat != "" {
			h.timeFormat = opts.TimeFormat
		}
		h.noColor = opts.NoColor
	}

	// Auto-detect terminal if not explicitly disabled
	if !h.noColor {
		if f, ok := out.(*os.File); ok {
			h.noColor = !color.ShouldColorize(f)
		}
	}

	return h
}

// Enabled reports whether the handler handles records at the given level.
func (h *ColorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle handles the Record.
func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Format time
	timeStr := r.Time.Format(h.timeFormat)

	// Format level with color
	levelStr := h.formatLevel(r.Level)

	// Format message
	msg := r.Message

	// Build output
	var buf []byte
	buf = append(buf, timeStr...)
	buf = append(buf, ' ')
	buf = append(buf, levelStr...)
	buf = append(buf, ' ')
	buf = append(buf, msg...)

	// Add group prefixes and pre-defined attrs
	for _, group := range h.groups {
		buf = append(buf, ' ')
		buf = append(buf, group...)
		buf = append(buf, '.')
	}

	for _, attr := range h.attrs {
		buf = h.appendAttr(buf, attr)
	}

	// Add record attrs
	r.Attrs(func(a slog.Attr) bool {
		buf = h.appendAttr(buf, a)
		return true
	})

	buf = append(buf, '\n')

	_, err := h.out.Write(buf)
	return err
}

// WithAttrs returns a new Handler with the given attributes.
func (h *ColorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	return &ColorHandler{
		out:        h.out,
		level:      h.level,
		timeFormat: h.timeFormat,
		noColor:    h.noColor,
		attrs:      newAttrs,
		groups:     h.groups,
		mu:         h.mu,
	}
}

// WithGroup returns a new Handler with the given group name.
func (h *ColorHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name

	return &ColorHandler{
		out:        h.out,
		level:      h.level,
		timeFormat: h.timeFormat,
		noColor:    h.noColor,
		attrs:      h.attrs,
		groups:     newGroups,
		mu:         h.mu,
	}
}

func (h *ColorHandler) formatLevel(level slog.Level) string {
	var levelText string
	var c color.Color

	switch {
	case level >= slog.LevelError:
		levelText = "ERR"
		c = color.Red
	case level >= slog.LevelWarn:
		levelText = "WRN"
		c = color.Yellow
	case level >= slog.LevelInfo:
		levelText = "INF"
		c = color.Green
	default:
		levelText = "DBG"
		c = color.Cyan
	}

	if h.noColor {
		return levelText
	}
	return c.Apply(levelText)
}

func (h *ColorHandler) appendAttr(buf []byte, a slog.Attr) []byte {
	if a.Equal(slog.Attr{}) {
		return buf
	}

	buf = append(buf, ' ')

	// Format key with color
	key := a.Key
	if !h.noColor {
		key = color.Cyan.Apply(key)
	}
	buf = append(buf, key...)
	buf = append(buf, '=')

	// Format value
	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindString:
		buf = append(buf, val.String()...)
	case slog.KindTime:
		buf = append(buf, val.Time().Format(time.RFC3339)...)
	case slog.KindDuration:
		buf = append(buf, val.Duration().String()...)
	default:
		buf = append(buf, fmt.Sprint(val.Any())...)
	}

	return buf
}
