package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func scanFixture(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestPNG(t, filepath.Join(dir, "shot.png"))
	if err := os.WriteFile(filepath.Join(dir, "memo.wav"), []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanQueryForAttachments(t *testing.T) {
	dir := scanFixture(t)
	png := filepath.Join(dir, "shot.png")
	wav := filepath.Join(dir, "memo.wav")
	tests := []struct {
		name, query string
		want        []string
	}{
		{"mid-sentence", "please explain shot.png in detail", []string{png}},
		{"trailing punctuation", "what is in shot.png?", []string{png}},
		{"quotes and paren", `look at "shot.png"), then (memo.wav).`, []string{png, wav}},
		{"missing file", "explain nothere.png", nil},
		{"extensionless", "explain shot", nil},
		{"no file token", "just a plain question", nil},
		{"duplicate mention", "shot.png and again shot.png", []string{png}},
		{"url ignored", "see https://example.com/shot.png", nil},
		{"unsupported ext", "see shot.txt", nil},
		{"dir named like image", "see adir.png", nil},
	}
	if err := os.Mkdir(filepath.Join(dir, "adir.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanQueryForAttachments(tc.query, dir)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestScanQueryForAttachments_OutsideWorkdirSkipped(t *testing.T) {
	dir := scanFixture(t)
	outside := scanFixture(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// absolute outside, ../ escape, and symlink escape are all skipped
	if err := os.Symlink(filepath.Join(outside, "shot.png"), filepath.Join(sub, "link.png")); err != nil {
		t.Skip("symlinks unavailable")
	}
	for _, q := range []string{
		"see " + filepath.Join(outside, "shot.png"),
		"see ../shot.png",
		"see link.png",
	} {
		if got := ScanQueryForAttachments(q, sub); len(got) != 0 {
			t.Errorf("query %q attached %v, want none", q, got)
		}
	}
	// absolute path inside workdir is fine
	if got := ScanQueryForAttachments("see "+filepath.Join(dir, "shot.png"), dir); len(got) != 1 {
		t.Errorf("absolute inside workdir: got %v", got)
	}
}

func TestScanQueryForAttachments_SizeCapAndNonRegular(t *testing.T) {
	dir := scanFixture(t)
	big := filepath.Join(dir, "big.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := ScanQueryForAttachments("see big.png", dir); len(got) != 0 {
		t.Errorf("oversized file attached: %v", got)
	}
}
