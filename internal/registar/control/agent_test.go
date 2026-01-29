package control_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"go.uber.org/goleak"
	"golang.org/x/sync/errgroup"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestAgent(t *testing.T) {
	public := netip.MustParseAddrPort("10.0.0.1:1234")
	private := netip.MustParseAddrPort("10.0.1.1:5678")

	t.Run("connect to", func(t *testing.T) {
		t.Run("connect successful", func(t *testing.T) {
			conn1, conn2 := quictest.Pipe(t)
			registar := control.NewController(conn1)
			agent := control.NewAgent()
			cmds := make(chan control.ConnectCommand, 1)
			agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
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
			conn1, conn2 := quictest.Pipe(t)
			registar := control.NewController(conn1)
			agent := control.NewAgent()
			agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
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

var errCodeServerClosed = quic.ApplicationErrorCode(0x1d3d3d)

func serverAgent(t *testing.T, agent *control.Agent, conn *quic.Conn) {
	var eg errgroup.Group
	eg.Go(func() error {
		return agent.Serve(conn)
	})

	t.Cleanup(func() {
		conn.CloseWithError(errCodeServerClosed, "test cleanup")
		if err := eg.Wait(); err != nil {
			if !isServerClosedErr(err) {
				t.Errorf("serve agent: %s", err)
			}
		}
	})
}

func isServerClosedErr(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) &&
		appErr.ErrorCode == errCodeServerClosed &&
		!appErr.Remote
}
