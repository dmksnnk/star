package control_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/registar/control"
	"golang.org/x/sync/errgroup"
)

func TestAgent(t *testing.T) {
	public := netip.MustParseAddrPort("10.0.0.1:1234")
	private := netip.MustParseAddrPort("10.0.1.1:5678")

	t.Run("request forward", func(t *testing.T) {
		conn1, conn2 := net.Pipe()
		registar := control.NewController(conn1)
		agent := control.NewAgent()
		cmds := make(chan control.RequestForwardCommand, 1)
		agent.OnRequestForward(func(rfc control.RequestForwardCommand) error {
			cmds <- rfc
			return nil
		})

		serverAgent(t, agent, conn2)

		if err := registar.DoRequestForward(context.TODO(), "test"); err != nil {
			t.Errorf("do request forward: %s", err)
		}

		want := control.RequestForwardCommand{
			PeerID: "test",
		}
		got := <-cmds
		if want != got {
			t.Errorf("unexpected command, want: %#v, got: %#v", want, got)
		}
	})

	t.Run("connect to", func(t *testing.T) {
		t.Run("connect successful", func(t *testing.T) {
			conn1, conn2 := net.Pipe()
			registar := control.NewController(conn1)
			agent := control.NewAgent()
			cmds := make(chan control.ConnectCommand, 1)
			agent.OnConnectTo(func(cmd control.ConnectCommand) (bool, error) {
				cmds <- cmd
				return true, nil
			})

			serverAgent(t, agent, conn2)

			if err := registar.ConnectTo(context.TODO(), public, private); err != nil {
				t.Errorf("do request forward: %s", err)
			}

			want := control.ConnectCommand{
				PublicAddress:  public,
				PrivateAddress: private,
			}
			got := <-cmds
			if want != got {
				t.Errorf("unexpected command, want: %#v, got: %#v", want, got)
			}
		})

		t.Run("connect failed", func(t *testing.T) {
			conn1, conn2 := net.Pipe()
			registar := control.NewController(conn1)
			agent := control.NewAgent()
			agent.OnConnectTo(func(cmd control.ConnectCommand) (bool, error) {
				return false, nil
			})

			serverAgent(t, agent, conn2)

			err := registar.ConnectTo(context.TODO(), public, private)
			if !errors.Is(err, control.ErrConnectFailed) {
				t.Errorf("unexpected error, want: %s, got: %s", control.ErrConnectFailed, err)
			}
		})
	})
}

func serverAgent(t *testing.T, agent *control.Agent, conn io.ReadWriteCloser) {
	var eg errgroup.Group
	eg.Go(func() error {
		return agent.Serve(conn)
	})

	t.Cleanup(func() {
		conn.Close()
		if err := eg.Wait(); err != nil {
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("wait: %s", err)
			}
		}
	})
}
