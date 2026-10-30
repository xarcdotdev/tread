package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesWithoutTemporaryFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	for _, data := range []string{"first", "second"} {
		if err := Write(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != data {
			t.Fatalf("got %q %v, want %q", got, err, data)
		}
	}
	st, err := os.Stat(filepath.Dir(path))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("parent directory mode: %v %v", st.Mode(), err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v %v", entries, err)
	}
}
