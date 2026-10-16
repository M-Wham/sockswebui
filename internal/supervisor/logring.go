package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogRingSize is how many lines each slot keeps for the Logs view.
const LogRingSize = 500

// LogLine is one redacted slot log record. Text is the full slog text form
// (time, level, msg and attrs); Msg and Level are split out for filtering.
type LogLine struct {
	Time  time.Time `json:"ts"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Text  string    `json:"text"`
}

// LogRing is a fixed-size, mutex-protected ring of the last LogRingSize lines.
type LogRing struct {
	mu    sync.Mutex
	lines [LogRingSize]LogLine
	next  int // index of the next write
	n     int // lines held, ≤ LogRingSize
}

func (r *LogRing) add(l LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = l
	r.next = (r.next + 1) % LogRingSize
	if r.n < LogRingSize {
		r.n++
	}
}

// Lines returns a copy of the held lines, oldest first.
func (r *LogRing) Lines() []LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LogLine, 0, r.n)
	start := (r.next - r.n + LogRingSize) % LogRingSize
	for i := 0; i < r.n; i++ {
		out = append(out, r.lines[(start+i)%LogRingSize])
	}
	return out
}

// Logs returns the slot's redacted log lines, oldest first.
func (s *Slot) Logs() []LogLine { return s.ring.Lines() }

// redactor replaces exact secret values with redactedMark.
type redactor []string

func newRedactor(secrets []string) redactor {
	var r redactor
	for _, v := range secrets {
		if v != "" {
			r = append(r, v)
		}
	}
	return r
}

func (r redactor) str(s string) string {
	for _, v := range r {
		s = strings.ReplaceAll(s, v, redactedMark)
	}
	return s
}

// attr redacts a's value: strings in place, groups recursively, and any
// other value whose printed form carries a secret becomes a redacted string.
func (r redactor) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.str(v.String()))
	case slog.KindGroup:
		as := v.Group()
		out := make([]any, len(as))
		for i, g := range as {
			out[i] = r.attr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		s := fmt.Sprint(v.Any())
		if red := r.str(s); red != s {
			return slog.String(a.Key, red)
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (r redactor) attrs(as []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = r.attr(a)
	}
	return out
}

// ringSink renders records to text for the ring; mu guards buf across the
// whole format-and-capture of one record.
type ringSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	ring *LogRing
}

// slotHandler is the slog handler every slot log record passes through: it
// redacts the record once, stores it in the slot ring and mirrors it to the
// process logger, so secrets never reach either.
type slotHandler struct {
	red  redactor
	sink *ringSink
	text slog.Handler // writes into sink.buf
	next slog.Handler // process logger
}

func newSlotHandler(secrets []string, ring *LogRing, next slog.Handler) *slotHandler {
	sink := &ringSink{ring: ring}
	return &slotHandler{
		red:  newRedactor(secrets),
		sink: sink,
		text: slog.NewTextHandler(&sink.buf, &slog.HandlerOptions{Level: slog.LevelInfo}),
		next: next,
	}
}

func (h *slotHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.text.Enabled(ctx, l) || h.next.Enabled(ctx, l)
}

func (h *slotHandler) Handle(ctx context.Context, r slog.Record) error {
	red := slog.NewRecord(r.Time, r.Level, h.red.str(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { red.AddAttrs(h.red.attr(a)); return true })
	if h.text.Enabled(ctx, r.Level) {
		h.sink.mu.Lock()
		h.sink.buf.Reset()
		_ = h.text.Handle(ctx, red)
		text := strings.TrimRight(h.sink.buf.String(), "\n")
		h.sink.mu.Unlock()
		h.sink.ring.add(LogLine{Time: r.Time, Level: r.Level.String(), Msg: red.Message, Text: text})
	}
	if h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, red)
	}
	return nil
}

func (h *slotHandler) WithAttrs(as []slog.Attr) slog.Handler {
	as = h.red.attrs(as)
	c := *h
	c.text, c.next = h.text.WithAttrs(as), h.next.WithAttrs(as)
	return &c
}

func (h *slotHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.text, c.next = h.text.WithGroup(name), h.next.WithGroup(name)
	return &c
}
