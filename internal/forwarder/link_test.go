package forwarder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/platform/udp"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestLink(t *testing.T) {
	udpSrv, udpClient := localUDPPipe(t)
	quicSrv, quicClient := quictest.Pipe(t)
	t.Cleanup(func() {
		quicClient.CloseWithError(0, "test cleanup")
		quicSrv.CloseWithError(0, "test cleanup")
	})

	ctx, cancel := context.WithCancel(context.Background())

	var eg errgroup.Group
	eg.Go(func() error {
		lc := linkConfig{}
		err := lc.link(ctx, udpSrv, newQUICConnDatagramConn(quicSrv))
		if !errors.Is(err, context.Canceled) { // expecting context canceled error
			return fmt.Errorf("link udpSrv and quicSrv: %w", err)
		}

		return nil
	})

	if _, err := udpClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write to UDP client: %s", err)
	}

	dg, err := quicClient.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatalf("receive datagram on QUIC client: %s", err)
	}

	if string(dg) != "hello" {
		t.Errorf("unexpected datagram, want: %q, got: %q", "hello", string(dg))
	}

	if err := quicClient.SendDatagram([]byte("hello again")); err != nil {
		t.Fatalf("send datagram on QUIC client: %s", err)
	}

	udpClient.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	n, err := udpClient.Read(buf)
	if err != nil {
		t.Fatalf("read from UDP client: %s", err)
	}

	if string(buf[:n]) != "hello again" {
		t.Errorf("unexpected UDP data, want: %q, got: %q", "hello again", string(buf[:n]))
	}

	cancel()

	if err := eg.Wait(); err != nil {
		t.Errorf("link error: %s", err)
	}
}

func TestLinkIdleTimeout(t *testing.T) {
	udpSrv, udpClient := localUDPPipe(t)
	quicSrv, quicClient := quictest.Pipe(t)
	t.Cleanup(func() {
		quicClient.CloseWithError(0, "test cleanup")
		quicSrv.CloseWithError(0, "test cleanup")
	})

	ctx := t.Context()

	lc := linkConfig{UDPIdleTimeout: 200 * time.Millisecond}

	var eg errgroup.Group
	eg.Go(func() error {
		return lc.link(ctx, udpSrv, newQUICConnDatagramConn(quicSrv))
	})

	// Verify link works by sending data through.
	if _, err := udpClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write to UDP client: %s", err)
	}

	dg, err := quicClient.ReceiveDatagram(ctx)
	if err != nil {
		t.Fatalf("receive datagram on QUIC client: %s", err)
	}

	if string(dg) != "hello" {
		t.Errorf("unexpected datagram, want: %q, got: %q", "hello", string(dg))
	}

	// Stop sending on UDP side, wait for idle timeout.
	err = eg.Wait()
	if !errors.Is(err, errLinkIdleTimeout) {
		t.Fatalf("expected errLinkIdleTimeout, got: %v", err)
	}
}

func localUDPPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	c1, c2, err := udp.Pipe()
	if err != nil {
		t.Fatalf("create UDP pipe: %s", err)
	}
	t.Cleanup(func() {
		if err := c1.Close(); err != nil {
			t.Errorf("close c1: %s", err)
		}
		if err := c2.Close(); err != nil {
			t.Errorf("close c2: %s", err)
		}
	})

	return c1, c2
}

type quicConnDatagramConn struct {
	*quic.Conn
}

func newQUICConnDatagramConn(conn *quic.Conn) quicConnDatagramConn {
	return quicConnDatagramConn{Conn: conn}
}

func (q quicConnDatagramConn) Close() error {
	return q.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "connection closed")
}
