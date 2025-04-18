package integrationtest_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/integrationtest"
	"github.com/dmksnnk/star/internal/platform"
	"golang.org/x/sync/errgroup"
)

func TestFakeGameServer(t *testing.T) {
	clientMessage := "ping"
	serverResponse := "pong"

	t.Run("call and handle", func(t *testing.T) {
		ll := &platform.LocalListener{}
		serverConn, err := ll.ListenUDP(context.TODO(), 0)
		if err != nil {
			t.Fatalf("listen local UDP: %s", err)
		}

		var eg errgroup.Group
		receivedMessages := make(chan string, 1)
		eg.Go(func() error {
			return integrationtest.Handle(serverConn, func(req string) string {
				receivedMessages <- req
				return serverResponse
			})
		})

		ld := &platform.LocalDialer{}
		clientConn, err := ld.DialUDP(context.TODO(), serverConn.LocalAddr().(*net.UDPAddr).Port)
		if err != nil {
			t.Fatalf("dial local UDP: %s", err)
		}

		for i := 0; i < 10; i++ {
			resp, err := integrationtest.Call(clientConn, clientMessage)
			if err != nil {
				t.Fatalf("call: %s", err)
			}

			if resp != serverResponse {
				t.Errorf("unexpected response, want: %s, got: %s", serverResponse, resp)
			}

			msg := wait(t, receivedMessages)
			if msg != clientMessage {
				t.Errorf("unexpected message, want: %s, got: %s", clientMessage, msg)
			}
		}

		if err := clientConn.Close(); err != nil {
			t.Fatalf("close client conn: %s", err)
		}
		if err := serverConn.Close(); err != nil {
			t.Fatalf("close server conn: %s", err)
		}

		if err := eg.Wait(); err != nil {
			t.Errorf("wait: %s", err)
		}
	})

	t.Run("server and client", func(t *testing.T) {
		receivedMessages := make(chan string, 1)
		srv := integrationtest.NewTestServer(t, func(req string) string {
			receivedMessages <- req
			return serverResponse
		})

		cl := integrationtest.NewTestClient(t, srv.Port())
		resp, err := cl.Call(clientMessage)
		if err != nil {
			t.Fatalf("call: %s", err)
		}

		if want, got := serverResponse, resp; want != got {
			t.Errorf("unexpected response, want: %s, got: %s", want, got)
		}

		msg := wait(t, receivedMessages)
		if want, got := clientMessage, msg; want != got {
			t.Errorf("unexpected message, want: %s, got: %s", clientMessage, msg)
		}
	})
}

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	var v T
	select {
	case v = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	return v
}
