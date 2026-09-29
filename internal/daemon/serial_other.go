//go:build !unix

package daemon

import "os"

// writeSerial: VaporOS is Linux; elsewhere there is no serial console.
func writeSerial(path, line string) error { return os.ErrNotExist }
