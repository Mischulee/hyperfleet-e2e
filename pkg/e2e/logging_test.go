package e2e

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	hfl "github.com/openshift-hyperfleet/hyperfleet-logger"
)

func TestGinkgoLogHandler_ForwardsRecord(t *testing.T) {
	var output bytes.Buffer
	handler := &GinkgoLogHandler{Handler: slog.NewJSONHandler(&output, nil)}
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "test log", 0)
	record.AddAttrs(slog.String("key", "value"))

	if err := handler.Handle(t.Context(), record); err != nil {
		t.Fatalf("handle record: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log entry: %v", err)
	}
	if got := entry["msg"]; got != "test log" {
		t.Errorf("message = %v, want test log", got)
	}
	if got := entry["key"]; got != "value" {
		t.Errorf("key = %v, want value", got)
	}
}

func TestNewLogHandler_SanitizesTextOutput(t *testing.T) {
	var output bytes.Buffer
	handler := newLogHandler(slog.LevelInfo, hfl.FormatText, &output)
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "SDK\tmessage\x1b[31m", 0)

	if err := handler.Handle(t.Context(), record); err != nil {
		t.Fatalf("handle record: %v", err)
	}

	line := strings.TrimSuffix(output.String(), "\n")
	if strings.ContainsAny(line, "\t\x1b") {
		t.Errorf("log contains unsanitized control characters: %q", line)
	}
}

func TestGinkgoLogHandler_PreservesWrapping(t *testing.T) {
	handler := &GinkgoLogHandler{Handler: slog.NewTextHandler(&bytes.Buffer{}, nil)}

	if _, ok := handler.WithAttrs([]slog.Attr{slog.String("key", "value")}).(*GinkgoLogHandler); !ok {
		t.Error("WithAttrs must preserve Ginkgo context injection")
	}
	if _, ok := handler.WithGroup("group").(*GinkgoLogHandler); !ok {
		t.Error("WithGroup must preserve Ginkgo context injection")
	}
}
