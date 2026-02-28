package registar

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"

	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
)

type AddrPair struct {
	Public  netip.AddrPort
	Private netip.AddrPort
}

type agent struct {
	str   *http3.Stream
	addrs AddrPair
}

func newAgent(str *http3.Stream, addrs AddrPair) agent {
	return agent{
		str:   str,
		addrs: addrs,
	}
}

func initP2P(host, peer agent) error {
	var hostErr, peerErr error
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		hostErr = control.ConnectTo(host.str, peer.addrs.Public, peer.addrs.Private)
	}()
	go func() {
		defer wg.Done()
		peerErr = control.ConnectTo(peer.str, host.addrs.Public, host.addrs.Private)
	}()

	wg.Wait()

	// If either succeeded, P2P worked
	if hostErr == nil && peerErr == nil {
		return nil
	}

	// Both must fail with ErrConnectFailed for P2P to be considered failed
	if errors.Is(hostErr, control.ErrConnectFailed) && errors.Is(peerErr, control.ErrConnectFailed) {
		return control.ErrConnectFailed
	}

	return joinErrors(hostErr, peerErr)
}

func initRelay(host, peer *http3.Stream, sessionID string) error {
	var hostErr, peerErr error
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		hostErr = control.ConnectViaRelay(host, sessionID)
	}()
	go func() {
		defer wg.Done()
		peerErr = control.ConnectViaRelay(peer, sessionID)
	}()

	wg.Wait()

	return joinErrors(hostErr, peerErr)
}

func joinErrors(hostErr, peerErr error) error {
	var err error
	if hostErr != nil {
		err = fmt.Errorf("host failed to connect: %w", hostErr)
	}
	if peerErr != nil {
		err = errors.Join(err, fmt.Errorf("peer failed to connect: %w", peerErr))
	}

	return err
}
