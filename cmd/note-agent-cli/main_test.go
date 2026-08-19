package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	appagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent"
)

func TestWriteEvent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := writeEvent(&stdout, &stderr, appagent.Event{Type: appagent.EventTextDelta, Delta: "你好"}, true); err != nil {
		t.Fatalf("writeEvent() error = %v", err)
	}
	if stdout.String() != "你好" || !strings.Contains(stderr.String(), "text.delta") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestWriteFailedEvent(t *testing.T) {
	want := errors.New("model unavailable")
	err := writeEvent(&bytes.Buffer{}, &bytes.Buffer{}, appagent.Event{Type: appagent.EventRunFailed, Err: want}, false)
	if !errors.Is(err, want) {
		t.Fatalf("writeEvent() error = %v", err)
	}
}
