package session

import (
	"strings"
	"sync"
)

type LogSink struct {
	mu     sync.Mutex
	lines  []string
	closed bool
	notify chan struct{}
}

func NewLogSink() *LogSink {
	return &LogSink{
		notify: make(chan struct{}),
	}
}

func (s *LogSink) WriteLine(line string) {
	if s == nil {
		return
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	s.lines = append(s.lines, line)
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *LogSink) ReadFrom(index int) ([]string, int, bool, <-chan struct{}) {
	if s == nil {
		return nil, 0, true, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 {
		index = 0
	}
	if index > len(s.lines) {
		index = len(s.lines)
	}

	entries := append([]string(nil), s.lines[index:]...)
	return entries, len(s.lines), s.closed, s.notify
}

func (s *LogSink) Close() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	s.closed = true
	close(s.notify)
}

func (s *LogSink) Content() string {
	if s == nil {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "")
}
