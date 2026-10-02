package update

import (
	"errors"
	"net"
	"net/url"
)

// ErrGaveUp marks a download that failed the way a network does (a
// connection lost, a stall, an early end, a busy server) on every attempt
// until it ran out of them: ErrRetriesExhausted under the name the
// extension code uses.
var ErrGaveUp = ErrRetriesExhausted

// IsNetworkError reports whether err says a source could not be reached,
// or kept failing the way a network does until the download gave up
// (ErrGaveUp), rather than answering: the same would happen to every other
// file. Not net.Error: a syscall.Errno is one, such as a missing file's.
// An early end of a file (io.ErrUnexpectedEOF) alone is not one: only a
// Remote source's could be the network's.
func IsNetworkError(err error) bool {
	var (
		oe *net.OpError
		de *net.DNSError
		ue *url.Error
	)
	return errors.As(err, &oe) || errors.As(err, &de) || errors.As(err, &ue) || errors.Is(err, ErrGaveUp)
}

// Remote reports whether s is reached over the network (a registry or an
// HTTP(S) directory), not read from a local directory or the live medium.
func (s *Source) Remote() bool { return s.f.remote() }
