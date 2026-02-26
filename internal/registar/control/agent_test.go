package control_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/dmksnnk/star/internal/platform/quictest"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/control/controltest"
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
		agent := control.NewAgent()
		cmds := make(chan control.ConnectCommand, 1)
		agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
			cmds <- cmd
			return true, nil
		})

		controltest.ServeAgent(t, agent, conn2)

		str, err := conn1.OpenStreamSync(context.TODO())
		if err != nil {
			t.Fatalf("open stream: %s", err)
		}

		if err := control.ConnectTo(str, public, private); err != nil {
			t.Errorf("connect to: %s", err)
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
		agent := control.NewAgent()
		agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
			return false, nil
		})

		controltest.ServeAgent(t, agent, conn2)

		str, err := conn1.OpenStreamSync(context.TODO())
		if err != nil {
			t.Fatalf("open stream: %s", err)
		}

		err = control.ConnectTo(str, public, private)
		if !errors.Is(err, control.ErrConnectFailed) {
			t.Errorf("unexpected error, want: %s, got: %s", control.ErrConnectFailed, err)
		}
	})
}
