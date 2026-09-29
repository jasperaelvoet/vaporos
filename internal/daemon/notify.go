package daemon

import (
	"net"
	"os"
)

// sdNotify sends a state string ("READY=1", "STOPPING=1") to systemd's
// notification socket, so vosd.service can be Type=notify and units ordered
// after it start once the API actually answers. Without NOTIFY_SOCKET (not
// run by systemd, or Type=simple) it does nothing. A leading "@" names an
// abstract socket, which Go's net package understands as-is.
func sdNotify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
