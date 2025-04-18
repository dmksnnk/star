package http3

import (
	"net"

	"github.com/quic-go/quic-go"
)

// StreamConn adapts a quic.Stream to the net.Conn interface.
type StreamConn struct {
	quic.Stream
	localAddr, remoteAddr net.Addr
}

// NewStreamConn creates a new StreamConn.
func NewStreamConn(s quic.Stream, localAddr, remoteAddr net.Addr) *StreamConn {
	return &StreamConn{
		Stream:     s,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}
}

func (c *StreamConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *StreamConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

var _ net.Conn = &StreamConn{}
