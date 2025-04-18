package control_test

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

func TestListener(t *testing.T) {
	t.Run("accept one", func(t *testing.T) {
		conn1, conn2 := net.Pipe()
		server := &fakeServer{}
		key := auth.NewKey()

		var eg errgroup.Group
		listener := control.Listen(conn1, server, key)
		eg.Go(func() error {
			if _, err := listener.AcceptForward(); err != nil {
				if errors.Is(err, net.ErrClosed) { // net.ErrClosed is returned when listener is closed
					return nil
				}
				return err
			}

			return nil
		})

		client := control.NewConnector(conn2)
		err := client.RequestForward(context.Background(), "peer1")
		if err != nil {
			t.Fatalf("RequestForward error: %v", err)
		}

		if err := listener.Close(); err != nil {
			t.Fatalf("listener close error: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !slices.Equal(server.connected[key], []string{"peer1"}) {
			t.Errorf("unexpected peer id, want peer1, got %q", server.connected[key])
		}
	})

	t.Run("accept multiple", func(t *testing.T) {
		conn1, conn2 := net.Pipe()
		server := &fakeServer{}
		key := auth.NewKey()

		var eg errgroup.Group
		listener := control.Listen(conn1, server, key)
		eg.Go(func() error {
			for {
				if _, err := listener.AcceptForward(); err != nil {
					if errors.Is(err, net.ErrClosed) { // net.ErrClosed is returned when listener is closed
						return nil
					}
					return err
				}
			}
		})

		var clientEg errgroup.Group
		client := control.NewConnector(conn2)
		for i := range 3 {
			clientEg.Go(func() error {
				return client.RequestForward(context.Background(), "peer"+strconv.Itoa(i))
			})
		}

		if err := clientEg.Wait(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if err := listener.Close(); err != nil {
			t.Fatalf("listener close error: %v", err)
		}

		if err := eg.Wait(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []string{"peer0", "peer1", "peer2"}
		got := server.connected[key]
		slices.Sort(got) // sort to ignore order
		if !slices.Equal(got, want) {
			t.Errorf("unexpected peer ids, want %q, got %q", want, got)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		_, conn2 := net.Pipe()

		client := control.NewConnector(conn2)
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		err := client.RequestForward(ctx, "peer1")
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("unexpected error, want %s, got %s", context.DeadlineExceeded, err)
		}
	})
}

type fakeServer struct {
	connected map[auth.Key][]string
}

func (s *fakeServer) Forward(ctx context.Context, key auth.Key, peerID string) (http3.RequestStream, error) {
	if s.connected == nil {
		s.connected = make(map[auth.Key][]string)
	}
	s.connected[key] = append(s.connected[key], peerID)

	return nil, nil
}
