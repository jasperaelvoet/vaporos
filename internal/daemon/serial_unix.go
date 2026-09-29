//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// writeSerial writes one line to a tty. O_NOCTTY keeps the port from
// becoming vosd's controlling terminal (a service has none, so the first
// tty it opens otherwise would), and O_NONBLOCK means a port stuck on flow
// control drops the line instead of hanging the announcer. O_APPEND does
// nothing on a tty but lets a plain file stand in for one. A leading
// newline starts the line fresh if the console was mid-line.
func writeSerial(path, line string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n" + line + "\n")
	return err
}
