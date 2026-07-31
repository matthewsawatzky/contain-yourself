package dockerworker

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workstation-manager/internal/config"
)

func TestCopyDockerLogStream(t *testing.T) {
	frame := func(stream byte, value string) []byte {
		size := len(value)
		return append([]byte{
			stream, 0, 0, 0,
			byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size),
		}, []byte(value)...)
	}
	input := append(frame(1, "first\n"), frame(2, "second\n")...)
	var output bytes.Buffer
	if err := copyDockerLogStream(bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "first\nsecond\n" {
		t.Fatalf("stream output = %q", output.String())
	}
	output.Reset()
	if err := copyDockerLogStream(strings.NewReader("raw\n"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "raw\n" {
		t.Fatalf("raw stream output = %q", output.String())
	}
}

func TestPersistentLogsAreBoundedAndTailed(t *testing.T) {
	root := t.TempDir()
	service := &Service{config: config.Worker{LogsDirectory: root}}
	directory := filepath.Join(root, "ws-abcdef1234")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "browser.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	output, ok := service.persistedLogs("ws-abcdef1234", "browser", 2)
	if !ok || output != "two\nthree\n" {
		t.Fatalf("persisted tail = %q, %v", output, ok)
	}
	if _, ok := service.persistedLogs("../../escape", "browser", 2); ok {
		t.Fatal("unsafe workstation log path was accepted")
	}
}

func TestRotatingLogWriterKeepsPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal.log")
	writer, err := newRotatingLogWriter(path, 8, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != "12345678" || string(current) != "new\n" {
		t.Fatalf("rotation previous=%q current=%q", previous, current)
	}
}
