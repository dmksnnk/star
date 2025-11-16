package registar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"time"

	"github.com/dmksnnk/star/internal/registar/control"
	"golang.org/x/sync/errgroup"
)

const (
	defaultConnectTimeout = time.Minute
)

type AddrPair struct {
	Public  netip.AddrPort
	Private netip.AddrPort
}

func Join(
	ctx context.Context,
	host, peer Peer,
	relayAddr netip.AddrPort,
) error {
	if err := initP2P(ctx, peer, host); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if err := initRelay(ctx, host, peer, relayAddr); err != nil {
				return fmt.Errorf("init relay connection: %w", err)
			}

			return nil
		}

		return fmt.Errorf("init P2P connection: %w", err)
	}

	return nil
}

func initP2P(ctx context.Context, host, peer Peer) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return host.controller.ConnectTo(ctx, peer.addrs.Public, peer.addrs.Private)
	})
	eg.Go(func() error {
		return peer.controller.ConnectTo(ctx, host.addrs.Public, host.addrs.Private)
	})

	return eg.Wait()
}

func initRelay(ctx context.Context, host, peer Peer, relayAddr netip.AddrPort) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return host.controller.ConnectTo(ctx, relayAddr, relayAddr)
	})
	eg.Go(func() error {
		return peer.controller.ConnectTo(ctx, relayAddr, relayAddr)
	})

	return eg.Wait()
}

type Peer struct {
	addrs      AddrPair
	controller *control.Controller
}

func NewPeer(addrs AddrPair, controlStream io.ReadWriter) Peer {
	return Peer{
		addrs:      addrs,
		controller: control.NewController(controlStream),
	}
}
