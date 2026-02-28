package forwarder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/dmksnnk/star/internal/registar"
	"golang.org/x/sync/errgroup"
)

const defaultUDPIdleTimeout = 10 * time.Second

// errLinkIdleTimeout is returned when no data has been received for the configured idle timeout.
var errLinkIdleTimeout = errors.New("idle timeout exceeded")

// linkConfig configures a bidirectional UDP-QUIC link.
type linkConfig struct {
	// UDPIdleTimeout is the maximum duration without receiving UDP data
	// before the link is considered idle and terminated.
	// Zero means no idle timeout.
	UDPIdleTimeout time.Duration
}

func (lc linkConfig) link(ctx context.Context, udpConn net.Conn, dgConn registar.DatagramConn) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		buf := make([]byte, 1500)
		for {
			n, err := lc.read(ctx, udpConn, buf)
			if err != nil {
				return fmt.Errorf("read from udp conn: %w", err)
			}

			if err := dgConn.SendDatagram(buf[:n]); err != nil {
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			dg, err := dgConn.ReceiveDatagram(ctx)
			if err != nil {
				return fmt.Errorf("receive datagram: %w", err)
			}

			if _, err := udpConn.Write(dg); err != nil {
				return fmt.Errorf("write datagram: %w", err)
			}
		}
	})

	return eg.Wait()
}

func (lc linkConfig) read(ctx context.Context, conn net.Conn, buf []byte) (int, error) {
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	lastRead := time.Now()
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		if lc.UDPIdleTimeout > 0 && time.Since(lastRead) > lc.UDPIdleTimeout {
			return 0, errLinkIdleTimeout
		}

		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := conn.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			return n, err
		}

		return n, nil
	}
}
