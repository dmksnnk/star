package integrationtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/dmksnnk/star/internal/platform"
	"golang.org/x/sync/errgroup"
)

// Handle handles UDP packets from a client and sends a response.
func Handle(conn net.PacketConn, handler func(req string) string) error {
	defer conn.Close()

	buf := make([]byte, platform.MTU)
	for {
		n, client, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}

		resp := handler(string(buf[:n]))

		if _, err := conn.WriteTo([]byte(resp), client); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
}

type Server struct {
	conn net.PacketConn
}

func NewTestUDPServer(t *testing.T, handler func(req string) string) *Server {
	t.Helper()

	conn, err := net.ListenPacket("udp", "localhost:0")
	if err != nil {
		t.Fatalf("listen local UDP: %s", err)
	}

	var eg errgroup.Group
	eg.Go(func() error {
		return Handle(conn, handler)
	})

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close UDP conn: %s", err)
		}

		if err := eg.Wait(); err != nil {
			t.Errorf("wait: %s", err)
		}
	})

	return &Server{
		conn: conn,
	}
}

func (s *Server) Port() int {
	return s.conn.LocalAddr().(*net.UDPAddr).Port
}

// Call sends a message to a server and returns the response.
func Call(conn net.Conn, msg string) (string, error) {
	if _, err := conn.Write([]byte(msg)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, platform.MTU)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	return string(buf[:n]), nil
}

type Client struct {
	Conn net.Conn
}

func NewTestUDPClient(t *testing.T, port int) *Client {
	t.Helper()

	ld := &platform.LocalDialer{}
	conn, err := ld.DialUDP(context.TODO(), port)
	if err != nil {
		t.Fatalf("dial local UDP: %s", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close client conn: %s", err)
		}
	})

	return &Client{Conn: conn}
}

func (c *Client) Call(msg string) (string, error) {
	return Call(c.Conn, msg)
}
