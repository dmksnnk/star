package controltest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

var errCodeServerClosed = quic.ApplicationErrorCode(0x1d3d3d)

func ServeAgent(t *testing.T, agent *control.Agent, conn *quic.Conn) {
	t.Helper()

	var eg errgroup.Group
	eg.Go(func() error {
		ctx := conn.Context()
		for {
			str, err := conn.AcceptStream(ctx)
			if err != nil {
				return fmt.Errorf("accept stream: %w", err)
			}

			eg.Go(func() error {
				return agent.Serve(str)
			})
		}
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
