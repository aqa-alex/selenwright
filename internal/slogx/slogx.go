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

type Config struct {
	JSON  bool
	Level slog.Level
	Out   io.Writer
}

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
		return "", "", nil
	}
	if len(parts) > 2 {
		rest = parts[2:]
	}
	return reqID, event, rest
}

func CaptureWriter(buf *bytes.Buffer, level slog.Level) io.Writer {
	h := &bracketJSONHandler{
		out:       buf,
		level:     level,
		writeLock: &sync.Mutex{},
	}
	return &slogWriter{h: h}
}
