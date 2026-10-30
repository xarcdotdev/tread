package drivers

import (
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/drivers/dft200"
)

func TestLookup(t *testing.T) {
	if d, err := Lookup("dft200"); err != nil || d != (dft200.Driver{}) {
		t.Fatalf("Lookup(dft200) = %#v, %v", d, err)
	}
	for _, name := range []string{"", "DFT200", "unknown"} {
		if _, err := Lookup(name); err == nil {
			t.Errorf("Lookup(%q) accepted", name)
		}
	}
}
