package main

import (
	"os"
	"testing"
)

func TestDaemonURLPersistence(t *testing.T) {
	// Clean up any existing daemon URL file before/after the test.
	removeDaemonURL()
	t.Cleanup(removeDaemonURL)

	const testURL = "ws://127.0.0.1:9222/devtools/browser/test-id"

	// Reading a non-existent URL file should fail.
	_, err := readDaemonURL()
	if err == nil {
		t.Fatal("expected error reading non-existent daemon URL, got nil")
	}

	// Write should succeed.
	if err := writeDaemonURL(testURL); err != nil {
		t.Fatalf("writeDaemonURL() failed: %v", err)
	}

	// Read should return the written URL.
	got, err := readDaemonURL()
	if err != nil {
		t.Fatalf("readDaemonURL() failed: %v", err)
	}
	if got != testURL {
		t.Fatalf("readDaemonURL() = %q, want %q", got, testURL)
	}

	// Remove should clean up.
	removeDaemonURL()
	_, err = readDaemonURL()
	if err == nil {
		t.Fatal("expected error reading removed daemon URL, got nil")
	}
}

func TestDaemonURLPath(t *testing.T) {
	p, err := daemonURLPath()
	if err != nil {
		t.Fatalf("daemonURLPath() failed: %v", err)
	}
	if p == "" {
		t.Fatal("daemonURLPath() returned empty string")
	}
}

func TestWriteDaemonURLCreatesDirectory(t *testing.T) {
	removeDaemonURL()
	t.Cleanup(removeDaemonURL)

	p, err := daemonURLPath()
	if err != nil {
		t.Fatalf("daemonURLPath() failed: %v", err)
	}

	// Remove the parent directory if it exists to test directory creation.
	_ = os.Remove(p)

	const testURL = "ws://127.0.0.1:9222/devtools/browser/dir-test"
	if err := writeDaemonURL(testURL); err != nil {
		t.Fatalf("writeDaemonURL() failed: %v", err)
	}

	got, err := readDaemonURL()
	if err != nil {
		t.Fatalf("readDaemonURL() failed after directory creation: %v", err)
	}
	if got != testURL {
		t.Fatalf("readDaemonURL() = %q, want %q", got, testURL)
	}
}

func TestReadDaemonURLRejectsEmpty(t *testing.T) {
	removeDaemonURL()
	t.Cleanup(removeDaemonURL)

	// Write an empty string.
	if err := writeDaemonURL(""); err != nil {
		t.Fatalf("writeDaemonURL() failed: %v", err)
	}

	_, err := readDaemonURL()
	if err == nil {
		t.Fatal("expected error reading empty daemon URL, got nil")
	}
}
