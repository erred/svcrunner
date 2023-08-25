package jsonlog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"sync"

	"go.opentelemetry.io/otel/trace"
)

const (
	// magic numbers to reduce number of slice resizes
	// slog holds 5 attrs
	stateBufferSize = 1024
)

// assert it is a handler
var _ slog.Handler = new(handler)

// reduce allocations in steady state
var pool = &sync.Pool{
	New: func() any {
		s := make([]byte, 0, stateBufferSize)
		return &s
	},
}

func New(level slog.Level, out io.Writer) slog.Handler {
	return &handler{
		minLevel: level,
		state:    new(state),
		mu:       new(sync.Mutex),
		w:        out,
	}
}

type handler struct {
	minLevel slog.Level
	state    *state
	mu       *sync.Mutex
	w        io.Writer
}

func (h *handler) clone() *handler {
	return &handler{
		minLevel: h.minLevel,
		state:    h.state.clone(),
		mu:       h.mu,
		w:        h.w,
	}
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.minLevel
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h2 := h.clone()
	for _, a := range attrs {
		h2.state.attr(a)
	}
	return h2
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := h.clone()
	h2.state.openGroup(name)
	return h2
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	// add attrs to state
	state := h.state.clone()
	r.Attrs(func(a slog.Attr) bool {
		state.attr(a)
		return true
	})
	state.closeAll()

	// initialize write buffer
	var buf []byte
	if cap(state.buf)-len(state.buf) < 160+len(r.Message) {
		buf = make([]byte, 0, len(state.buf)+160+len(r.Message))
	} else {
		buf = *pool.Get().(*[]byte)
	}
	defer func() { pool.Put(&buf) }()

	buf = append(buf, `{`...)

	// time
	if !r.Time.IsZero() {
		buf = append(buf, `"time":`...)
		buf = append(buf, jsonBytes(r.Time)...)
		buf = append(buf, `,`...)
	}
	// level
	buf = append(buf, `"level":`...)
	buf = append(buf, jsonBytes(r.Level)...)

	// trace
	spanCtx := trace.SpanContextFromContext(ctx)
	if spanCtx.IsValid() {
		buf = append(buf, `,"trace_id":"`...)
		buf = append(buf, spanCtx.TraceID().String()...)
		buf = append(buf, `","span_id":"`...)
		buf = append(buf, spanCtx.SpanID().String()...)
		buf = append(buf, `"`...)

	}
	// any other special keys
	// e.g. file:line, attrs from ctx or extracted during attr processing by state.attr

	// message
	buf = append(buf, `,"message":`...)
	buf = append(buf, jsonBytes(r.Message)...)

	// attrs
	if len(state.buf) > 0 {
		buf = append(buf, `,`...)
		buf = append(buf, state.buf...)
	}
	buf = append(buf, "}\n"...)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func jsonBytes(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// state holds preformatted attributes
type state struct {
	confirmedLast int    // length of buf when we last wrote a complete attr
	groupOpenIdx  []int  // indexes before open groups, allows rollback on empty groups
	separator     []byte // separator to write before an attr or group
	buf           []byte // buffer of preformatted contents
	// TODO hold special keys to be placed in top level (eg error)
}

func (h *state) clone() *state {
	var buf []byte
	if cap(h.buf) > stateBufferSize {
		buf = slices.Clone(h.buf)
	} else {
		buf = *pool.Get().(*[]byte)
		buf = buf[:len(h.buf)]
	}
	copy(buf, h.buf)
	s := &state{
		h.confirmedLast,
		slices.Clone(h.groupOpenIdx),
		slices.Clone(h.separator),
		buf,
	}
	runtime.SetFinalizer(s, func(s *state) {
		pool.Put(&buf)
	})
	return s
}

func (h *state) openGroup(n string) {
	h.groupOpenIdx = append(h.groupOpenIdx, len(h.buf)) // record rollback point
	h.buf = append(h.buf, h.separator...)               // maybe need a separator
	h.buf = append(h.buf, jsonBytes(n)...)              // key name
	h.buf = append(h.buf, []byte(":{")...)              // open group
	h.separator = nil                                   // no separator for first attr
}

func (h *state) closeGroup() {
	lastGroupIdx := h.groupOpenIdx[len(h.groupOpenIdx)-1] // pop off the rollback point for current group
	h.groupOpenIdx = h.groupOpenIdx[:len(h.groupOpenIdx)-1]
	if h.confirmedLast > lastGroupIdx { // group was non empty
		h.buf = append(h.buf, []byte("}")...) // close off the group
		h.confirmedLast = len(h.buf)          // record new last point
		return
	}
	h.buf = h.buf[:lastGroupIdx] // all open subgroups were empty, rollback
}

func (h *state) closeAll() {
	for range h.groupOpenIdx {
		h.closeGroup()
	}
	h.groupOpenIdx = nil
}

func (h *state) attr(attr slog.Attr) {
	val := attr.Value.Resolve()  // handle logvaluer
	if attr.Equal(slog.Attr{}) { // drop empty attr
		return
	} else if val.Kind() == slog.KindGroup { // recurse into group
		g := val.Group()
		if len(g) == 0 {
			return
		} else if attr.Key != "" { // inline empty keys
			h.openGroup(attr.Key)
		}
		for _, a := range val.Group() {
			h.attr(a)
		}
		if attr.Key != "" {
			h.closeGroup()
		}
		return
	} else if attr.Key == "" {
		return
	}
	// TODO: grab any special keys

	h.buf = append(h.buf, h.separator...)
	h.separator = []byte(",")
	h.buf = append(h.buf, jsonBytes(attr.Key)...)
	h.buf = append(h.buf, []byte(":")...)
	h.buf = append(h.buf, jsonBytes(val.Any())...)
	h.confirmedLast = len(h.buf)
}
