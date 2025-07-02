package http3platform

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
)

// Stream is interface that is implemented by [http3.RequestStream] and [http3.Stream].
type Stream interface {
	io.ReadWriteCloser
	CancelRead(quic.StreamErrorCode)
	CancelWrite(quic.StreamErrorCode)
	Context() context.Context
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

var _ control.Conn = &StreamConn{}

// StreamConn is a wrapper around [Stream] that implements [net.Conn] interface.
type StreamConn struct {
	Stream
}

// NewStreamConn creates a new [StreamConn].
func NewStreamConn(s Stream) *StreamConn {
	return &StreamConn{
		Stream: s,
	}
}

func (s *StreamConn) Read(p []byte) (n int, err error) {
	n, err = s.Stream.Read(p)
	if err != nil {
		if errcode.IsLocalHTTPError(err, errcode.Cancelled) {
			return n, net.ErrClosed
		}

		return n, err
	}
	return n, err
}

func (s *StreamConn) Write(p []byte) (n int, err error) {
	n, err = s.Stream.Write(p)
	if err != nil {
		if errcode.IsLocalHTTPError(err, errcode.Cancelled) {
			return n, net.ErrClosed
		}

		return n, err
	}
	return n, err
}

// Close closes the stream connection.
func (s *StreamConn) Close() error {
	s.CancelRead(errcode.Cancelled)
	s.CancelWrite(errcode.Cancelled)
	return nil
}
