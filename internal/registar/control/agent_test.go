package control_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/control/controltest"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestAgent(t *testing.T) {
	public := netip.MustParseAddrPort("10.0.0.1:1234")
	private := netip.MustParseAddrPort("10.0.1.1:5678")

	t.Run("connect successful", func(t *testing.T) {
		conn1, conn2 := quictest.Pipe(t)
		registar := control.NewController(conn1)
		agent := control.NewAgent()
		cmds := make(chan control.ConnectCommand, 1)
		agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
			cmds <- cmd
			return true, nil
		})

		controltest.ServeAgent(t, agent, conn2)

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

		controltest.ServeAgent(t, agent, conn2)

		err := registar.ConnectTo(context.TODO(), public, private)
		if !errors.Is(err, control.ErrConnectFailed) {
			t.Errorf("unexpected error, want: %s, got: %s", control.ErrConnectFailed, err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		conn1, conn2 := quictest.Pipe(t)

		controller := control.NewController(conn1)

		agent := control.NewAgent()
		handlerStarted := make(chan struct{})
		agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
			close(handlerStarted)
			<-ctx.Done() // block until context is cancelled
			return false, ctx.Err()
		})

		controltest.ServeAgent(t, agent, conn2)

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- controller.ConnectTo(ctx, public, private)
		}()

		<-handlerStarted
		cancel()

		err := <-errCh
		if !isRequestCanceledErr(err) {
			t.Errorf("expected local request canceled error, got: %T", err)
			return
		}
	})
}

func isRequestCanceledErr(err error) bool {
	var streamErr *quic.StreamError
	return errors.As(err, &streamErr) &&
		streamErr.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestCanceled) &&
		!streamErr.Remote
}
