// Package slogx routes the stdlib log package through log/slog so a
// single -log-json flag can flip the entire process's output from the
// legacy bracketed-text format to structured JSON without touching
// any of the ~150 log.Printf call sites scattered across the
// codebase.
//
// The bracketed format selenwright has used since upstream Selenoid
// is already structured — positionally — so when JSON mode is on,
// the handler parses `[requestId] [EVENT] [field1] [field2] ...`
// back into request_id / event / fields triples and emits them as
// proper JSON attributes. When JSON mode is off, Install is a no-op
// and the default log.Printf output path is left untouched so
// operator tail views look identical to pre-slog builds.
package slogx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config controls how Install wires slog into the process.
type Config struct {
	// JSON enables JSON output. When false Install is a no-op.
	JSON bool
	// Level sets the minimum slog level written to Out. Defaults to
	// slog.LevelInfo when zero.
	Level slog.Level
	// Out is the destination writer. Defaults to os.Stderr when nil.
	Out io.Writer
}

// Install wires the process logging according to cfg. Safe to call
// once at startup; calling twice replaces the previous routing.
func Install(cfg Config) {
	if !cfg.JSON {
		return
	}
	out := cfg.Out
	if out == nil {
		out = os.Stderr
	}
	h := &bracketJSONHandler{
		out:       out,
		level:     cfg.Level,
		writeLock: &sync.Mutex{},
	}
	slog.SetDefault(slog.New(h))
	log.SetFlags(0)
	log.SetOutput(&slogWriter{h: h})
}

// slogWriter implements io.Writer by forwarding each line written by
// the stdlib log package into the slog handler as an Info record. The
// stdlib log package always writes one log line per Write call, so
// there's no framing work to do here.
type slogWriter struct {
	h slog.Handler
}

func (w *slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
	if err := w.h.Handle(context.Background(), r); err != nil {
		return 0, err
	}
	return len(p), nil
}

// bracketJSONHandler renders records as one-line JSON, parsing the
// legacy bracketed message format into request_id / event / fields
// attributes when it matches. The mutex is lifted into a shared
// writeLock so WithAttrs can safely clone the handler; copying a
// sync.Mutex value would trip go vet.
type bracketJSONHandler struct {
	out       io.Writer
	level     slog.Level
	writeLock *sync.Mutex
	attrs     []slog.Attr
}

func (h *bracketJSONHandler) Enabled(_ context.Context, level slog.Level) bool {
	min := h.level
	return level >= min
}

func (h *bracketJSONHandler) Handle(_ context.Context, r slog.Record) error {
	obj := make(map[string]any, 4+r.NumAttrs())
	obj["time"] = r.Time.UTC().Format(time.RFC3339Nano)
	obj["level"] = r.Level.String()

	reqID, event, rest := parseBracketedMessage(r.Message)
	if event != "" {
		obj["event"] = event
		if reqID != "" {
			obj["request_id"] = reqID
		}
		if len(rest) > 0 {
			obj["fields"] = rest
		}
	} else {
		obj["msg"] = r.Message
	}

	for _, a := range h.attrs {
		obj[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		obj[a.Key] = a.Value.Any()
		return true
	})

	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	h.writeLock.Lock()
	defer h.writeLock.Unlock()
	_, err = h.out.Write(b)
	return err
}

func (h *bracketJSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &bracketJSONHandler{
		out:       h.out,
		level:     h.level,
		writeLock: h.writeLock,
		attrs:     cloned,
	}
}

func (h *bracketJSONHandler) WithGroup(_ string) slog.Handler {
	return h
}

// parseBracketedMessage splits a message that looks like
// "[42] [SESSION_CREATED] [id] [count]" into ("42",
// "SESSION_CREATED", ["id", "count"]). Messages not in that shape
// return ("", "", nil) and the handler falls back to emitting the
// full string as msg.
func parseBracketedMessage(msg string) (reqID, event string, rest []string) {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "[") {
		return "", "", nil
	}
	var parts []string
	for {
		open := strings.IndexByte(msg, '[')
		if open == -1 {
			break
		}
		close := strings.IndexByte(msg[open+1:], ']')
		if close == -1 {
			break
		}
		parts = append(parts, msg[open+1:open+1+close])
		msg = msg[open+1+close+1:]
		msg = strings.TrimLeft(msg, " ")
	}
	if len(parts) < 2 {
		return "", "", nil
	}
	reqID = parts[0]
	event = parts[1]
	if reqID == "-" {
		reqID = ""
	} else if _, err := strconv.Atoi(reqID); err != nil {
		// The first bracket wasn't a request id sentinel; treat the
		// whole thing as an unstructured message rather than
		// inventing a bogus request_id field.
		return "", "", nil
	}
	if len(parts) > 2 {
		rest = parts[2:]
	}
	return reqID, event, rest
}

// CaptureWriter returns a writer the tests can feed log.Printf output
// through to assert JSON shape without redirecting os.Stderr.
func CaptureWriter(buf *bytes.Buffer, level slog.Level) io.Writer {
	h := &bracketJSONHandler{
		out:       buf,
		level:     level,
		writeLock: &sync.Mutex{},
	}
	return &slogWriter{h: h}
}
