package slogx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func logOutput(t *testing.T, msg string) (map[string]any, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := CaptureWriter(buf, slog.LevelInfo)
	_, err := w.Write([]byte(msg + "\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw := buf.String()
	if strings.TrimSpace(raw) == "" {
		return nil, raw
	}
	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("json.Unmarshal %q: %v", raw, err)
	}
	return obj, raw
}

func TestNumericRequestIdIsParsed(t *testing.T) {
	obj, raw := logOutput(t, "[42] [SESSION_CREATED] [quota-a] [attempt-1]")
	if obj["event"] != "SESSION_CREATED" {
		t.Fatalf("event wrong: %s", raw)
	}
	if obj["request_id"] != "42" {
		t.Fatalf("request_id wrong: %s", raw)
	}
	fields, ok := obj["fields"].([]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields wrong: %s", raw)
	}
	if fields[0] != "quota-a" || fields[1] != "attempt-1" {
		t.Fatalf("fields mismatched: %s", raw)
	}
}

func TestSentinelRequestIdEmits_NoRequestIdKey(t *testing.T) {
	obj, raw := logOutput(t, "[-] [INIT] [Something happened]")
	if obj["event"] != "INIT" {
		t.Fatalf("event wrong: %s", raw)
	}
	if _, present := obj["request_id"]; present {
		t.Fatalf("request_id should be absent for '-': %s", raw)
	}
}

func TestNonBracketedMessageFallsBackToMsg(t *testing.T) {
	obj, raw := logOutput(t, "plain human sentence")
	if obj["msg"] != "plain human sentence" {
		t.Fatalf("msg wrong: %s", raw)
	}
	if _, hasEvent := obj["event"]; hasEvent {
		t.Fatalf("event should be absent: %s", raw)
	}
}

func TestMalformedBracketFallsBackToMsg(t *testing.T) {
	obj, raw := logOutput(t, "[oops] [EVENT] [x]")
	if obj["msg"] != "[oops] [EVENT] [x]" {
		t.Fatalf("should fall through: %s", raw)
	}
}

func TestLevelFieldPresent(t *testing.T) {
	obj, raw := logOutput(t, "[-] [INIT] [x]")
	if obj["level"] != "INFO" {
		t.Fatalf("level wrong: %s", raw)
	}
	if obj["time"] == "" {
		t.Fatalf("time missing: %s", raw)
	}
}
