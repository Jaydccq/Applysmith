package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The transcript folder holds one PDF: an upload replaces it, a removal
// empties it, and a refused upload leaves the stored one as it was.
func TestTranscriptIsOnePDFThatAnUploadReplacesAndARemovalDeletes(t *testing.T) {
	w := newWorkspace(t)
	if got, data, err := w.ReadTranscript(); err != nil || got != "" || data != nil {
		t.Fatalf("a fresh workspace holds a transcript: %+v %v", got, err)
	}
	first := []byte("%PDF-1.4 first")
	if _, err := w.StoreTranscript("fall-term", first); err != nil {
		t.Fatal(err)
	}
	second := []byte("%PDF-1.7 the second one")
	stored, err := w.StoreTranscript("unofficial-transcript", second)
	if err != nil || stored != "unofficial-transcript.pdf" {
		t.Fatalf("%q %v", stored, err)
	}
	got, data, err := w.ReadTranscript()
	if err != nil || got != stored || !bytes.Equal(data, second) {
		t.Fatalf("read %q %q %v", got, data, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.Root, TranscriptDir)); len(entries) != 1 {
		t.Fatalf("the replaced transcript was kept: %v", entries)
	}

	for _, c := range []struct {
		name, file string
		data       []byte
	}{
		{"not a PDF", "notes", []byte("plain text")},
		{"too large", "big", append([]byte("%PDF-"), make([]byte, maxResumeBytes)...)},
		{"a path for a name", "../escape", first},
		{"capitals", "Fall", first},
	} {
		if _, err := w.StoreTranscript(c.file, c.data); err == nil {
			t.Errorf("%s: stored", c.name)
		}
	}
	if got, data, _ := w.ReadTranscript(); got != stored || !bytes.Equal(data, second) {
		t.Fatalf("a refused upload changed the transcript: %q", got)
	}

	if err := w.RemoveTranscript(); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.Transcript(); got != "" {
		t.Fatalf("removed, still %q", got)
	}
	if err := w.RemoveTranscript(); err != nil {
		t.Fatalf("removing no transcript: %v", err)
	}
}
