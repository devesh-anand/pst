package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLogsCarryLevel(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newHandler("json", &buf)).Info("pst listening", "addr", ":8080")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not JSON: %q", buf.String())
	}
	if line["level"] != "INFO" || line["msg"] != "pst listening" || line["addr"] != ":8080" {
		t.Fatalf("line = %v", line)
	}
}

func TestTextLogs(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newHandler("text", &buf)).Info("hello")
	if !strings.Contains(buf.String(), "level=INFO msg=hello") {
		t.Fatalf("text log = %q", buf.String())
	}
}

func TestAutoFormatIsJSONWhenNotATerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	newLogger("", f).Info("hi")
	b, _ := os.ReadFile(f.Name())
	if !json.Valid(bytes.TrimSpace(b)) {
		t.Fatalf("auto format to a file should be JSON, got %q", b)
	}
}
