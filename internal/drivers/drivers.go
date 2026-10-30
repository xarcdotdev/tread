// Package drivers maps treadmill.driver names to drivers. Each driver is a
// subpackage; add it to registry to make it configurable.
package drivers

import (
	"fmt"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/drivers/dft200"
)

var registry = map[string]device.Driver{
	"dft200": dft200.Driver{},
}

func Lookup(name string) (device.Driver, error) {
	if d, ok := registry[name]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("unknown treadmill driver %q", name)
}
