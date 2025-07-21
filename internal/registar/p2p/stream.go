package p2p

import (
	"context"
	"fmt"

	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

const maxQuarterStreamID = 1<<60 - 1

type Stream struct {
	conn *quic.Conn
	*quic.Stream
}

var _ http3platform.Stream = &Stream{}

func newStream(conn *quic.Conn, stream *quic.Stream) *Stream {
	return &Stream{
		conn:   conn,
		Stream: stream,
	}
}

func (s *Stream) Context() context.Context {
	return s.Stream.Context()
}

// SendDatagram sends a datagram on a stream.
func (s *Stream) SendDatagram(b []byte) error {
	data := make([]byte, 0, len(b)+8)
	data = quicvarint.Append(data, uint64(s.StreamID()/4))
	data = append(data, b...)
	return s.conn.SendDatagram(data)
}

// ReceiveDatagram receives a datagram on the stream.
// Copied from https://github.com/quic-go/quic-go/blob/893a5941fbb077255d14e93dd5b5c13c7e05fe4a/http3/conn.go#L391-L423
func (s *Stream) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	for {
		b, err := s.conn.ReceiveDatagram(ctx)
		if err != nil {
			return nil, err
		}
		quarterStreamID, n, err := quicvarint.Parse(b)
		if err != nil {
			s.conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeDatagramError), "")
			return nil, fmt.Errorf("could not read quarter stream id: %w", err)
		}
		if quarterStreamID > maxQuarterStreamID {
			s.conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeDatagramError), "")
			return nil, fmt.Errorf("invalid quarter stream id: %w", err)
		}
		streamID := 4 * quarterStreamID
		if uint64(s.StreamID()) != streamID {
			continue // unknown stream
		}

		return b[n:], nil
	}
}
