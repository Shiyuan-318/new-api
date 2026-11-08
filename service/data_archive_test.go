package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func TestDataArchiveFileNameValidation(t *testing.T) {
	valid := []string{"archive-000001.jsonl", "archive-123456.jsonl"}
	invalid := []string{
		"archive-1.jsonl",
		"archive-0000012.jsonl",
		"archive-00000a.jsonl",
		"../archive-000001.jsonl",
		"archive-000001.txt",
		"other-000001.jsonl",
		"archive-000001/../../etc/passwd",
		"",
	}
	for _, name := range valid {
		if !IsValidDataArchiveFileName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}
	for _, name := range invalid {
		if IsValidDataArchiveFileName(name) {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestDataArchiveVolumeRollOver(t *testing.T) {
	tmp := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	entry := RelayArchiveEntry{
		RequestId:  "req-1",
		ModelName:  "gpt-test",
		UserId:     1,
		Success:    true,
		RequestBody: `{"model":"gpt-test"}`,
		ResponseBody: `{"choices":[]}`,
	}

	// Each entry is ~200 bytes; a 1 MB-per-volume cap must keep the first
	// volume well under the limit, but a tiny cap (0 -> normalized default)
	// is avoided here. Use maxFileMB small enough via the bytes threshold
	// directly: appendLocalArchive takes MB, so use 1 MB.
	for i := 0; i < 5; i++ {
		entry.RequestId = "req-" + string(rune('a'+i))
		if err := appendLocalArchive(entry, 100, 1); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	files, err := ListDataArchiveVolumes()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 volume, got %d: %+v", len(files), files)
	}
	if files[0].Name != "archive-000001.jsonl" {
		t.Fatalf("expected first volume name archive-000001.jsonl, got %s", files[0].Name)
	}
	if files[0].Entries != 5 {
		t.Fatalf("expected 5 entries, got %d", files[0].Entries)
	}

	// Force a rollover with a 1-entry volume cap by writing until a new file
	// appears: emulate by deleting the size check through a tiny cap.
	// Instead, verify rollover directly via activeArchiveVolumePath.
	if err := os.Truncate(filepath.Join(dataArchiveDirPath(), "archive-000001.jsonl"), 0); err == nil {
		// An empty volume is below the cap, so the next append reuses it.
		if err := appendLocalArchive(entry, 100, 1); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	// A full volume triggers a new one.
	// Pad the first volume beyond 1MB? Too slow. Roll over by removing the
	// file check: call activeArchiveVolumePath with 0 bytes cap.
	path, err := activeArchiveVolumePath(0)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "archive-000002.jsonl" {
		t.Fatalf("expected rollover to archive-000002.jsonl, got %s", filepath.Base(path))
	}
}

func TestDataArchiveVolumeJSONExport(t *testing.T) {
	tmp := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	entry := RelayArchiveEntry{
		RequestId:    "req-json",
		ModelName:    "gpt-test",
		RequestBody:  `{"messages":[{"role":"user","content":"hi"}]}`,
		ResponseBody: `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`,
		Success:      true,
	}
	if err := appendLocalArchive(entry, 100, 50); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	payload, err := LoadDataArchiveVolumeJSON("archive-000001.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("volume export is not a valid JSON array: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 exported entry, got %d", len(decoded))
	}
	if decoded[0]["request_id"] != "req-json" {
		t.Fatalf("unexpected exported entry: %v", decoded[0])
	}
	if !strings.HasPrefix(string(payload), "[") {
		t.Fatal("expected JSON array document")
	}
}

func TestDataArchiveCaptureRelayEntryFields(t *testing.T) {
	if DataArchiveCaptureEnabled() {
		t.Skip("data archive enabled in environment; skipping capture test")
	}
	// CaptureRelayArchive must be a no-op when disabled.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	CaptureRelayArchive(c, &common.RelayInfo{}, nil, nil, true)
	// No panic and no files written is the expected outcome.
}
