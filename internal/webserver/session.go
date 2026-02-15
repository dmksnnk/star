package webserver

import (
	"net"

	"github.com/dmksnnk/star/internal/registar/auth"
)

type activeSession struct {
	kind string // "host" or "peer"
	info sessionInfo

	done chan struct{} // closed to signal the background work to stop
}

type sessionInfo struct {
	InviteCode    auth.Key     // host only
	GameAddress   string       // host only
	ListenAddress *net.UDPAddr // peer only
}

// stop signals the session to shut down.
func (s *activeSession) stop() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}
