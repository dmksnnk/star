package forwarder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

func link(ctx context.Context, udpConn net.Conn, quicConn *quic.Conn) error {
	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		buf := make([]byte, 1500)
		for {
			n, err := read(ctx, udpConn, buf)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("read from udp conn: %w", err)
			}

			if err := quicConn.SendDatagram(buf[:n]); err != nil {
				return fmt.Errorf("send datagram: %w", err)
			}
		}
	})

	eg.Go(func() error {
		for {
			dg, err := quicConn.ReceiveDatagram(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("receive datagram: %w", err)
			}

			if _, err := udpConn.Write(dg); err != nil {
				return fmt.Errorf("write datagram: %w", err)
			}
		}
	})

	return eg.Wait()
}

func read(ctx context.Context, conn net.Conn, buf []byte) (int, error) {
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
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
