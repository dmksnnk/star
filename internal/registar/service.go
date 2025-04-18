package registar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform"
	http3platform "github.com/dmksnnk/star/internal/platform/http3"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go/http3"
)

// ErrInvalidToken is returned when a token is not valid.
var ErrInvalidToken = errors.New("invalid token")

// Registar is a service for game registration.
type Registar struct {
	hosts          *platform.Map[auth.Key, *control.Connector]
	waitingPeers   *platform.Map2[auth.Key, string, http3.Stream]
	connectedPeers *platform.Map2[auth.Key, string, http3.Stream]

	logger *slog.Logger
	wg     sync.WaitGroup
}

// New creates a new registar service.
func New() *Registar {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	logger = logger.With("source", "registar")

	return &Registar{
		hosts:          platform.NewMap[auth.Key, *control.Connector](),
		waitingPeers:   platform.NewMap2[auth.Key, string, http3.Stream](),
		connectedPeers: platform.NewMap2[auth.Key, string, http3.Stream](),
		logger:         logger,
	}
}

// RegisterGame registers a game with the given server address.
func (s *Registar) RegisterGame(ctx context.Context, key auth.Key, conn *http3platform.StreamConn) {
	s.hosts.Put(key, control.NewConnector(conn))

	// watch for connection close
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ctx := conn.Context()
		<-ctx.Done()

		err := context.Cause(ctx)
		var peers []string
		s.connectedPeers.ForEach(key, func(peerID string, stream http3.Stream) {
			stream.Close()
			peers = append(peers, peerID)
		})

		for _, peerID := range peers {
			s.connectedPeers.Delete(key, peerID, err)
		}

		s.hosts.Delete(key, err)
	}()
}

// GameExists returns true if a game with the given key exists.
func (s *Registar) GameExists(key auth.Key) bool {
	_, exists := s.hosts.Get(key)
	return exists
}

// ConnectGame connects a game to the server.
func (s *Registar) ConnectGame(ctx context.Context, key auth.Key, peerID string, peerStream http3.Stream) {
	hostControl, ok := s.hosts.Get(key)
	if !ok {
		peerStream.CancelRead(errcode.HostClosed)
		s.logger.Error("host not found", "key", key.String())
		return
	}

	s.waitingPeers.Put(key, peerID, peerStream)
	defer s.waitingPeers.Delete(key, peerID, nil)

	if err := hostControl.RequestForward(ctx, peerID); err != nil {
		peerStream.CancelRead(errcode.HostInternalError)
		s.logger.Error("request host to forward", "error", err)
		return
	}
}

// PeerExists returns true if a peer exists.
func (s *Registar) PeerExists(key auth.Key, peerID string) bool {
	_, ok := s.connectedPeers.Get(key, peerID)
	return ok
}

// WaitingPeerExists returns true if a peer waiting for forwarding exists.
func (s *Registar) WaitingPeerExists(key auth.Key, peerID string) bool {
	_, ok := s.waitingPeers.Get(key, peerID)
	return ok
}

// Forward forwards peer's conn to the game host conn.
func (s *Registar) Forward(ctx context.Context, key auth.Key, peerID string, hostStream http3.Stream) {
	peerStream, ok := s.waitingPeers.Get(key, peerID)
	if !ok {
		hostStream.CancelRead(errcode.PeerClosed)
		s.logger.Error("waiting peer not found", "key", key.String(), "peer_id", peerID)
		return
	}

	defer s.connectedPeers.Put(key, peerID, peerStream)

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		// use peer context, so when it's done, the host context is also done
		err := s.forwardToHost(peerStream.Context(), hostStream, peerStream)
		s.connectedPeers.Delete(key, peerID, err)
	}()

	go func() {
		defer s.wg.Done()
		// use host context, so when it's done, the peer context is also done
		err := s.forwardToPeer(hostStream.Context(), peerStream, hostStream)
		s.connectedPeers.Delete(key, peerID, err)
	}()
}

func (s *Registar) forwardToHost(ctx context.Context, hostConn, peerConn http3.Stream) error {
	for {
		dg, err := peerConn.ReceiveDatagram(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				hostConn.CancelRead(errcode.Cancelled)
				return nil
			}
			if errcode.IsRemoteQUICConnClosed(err) {
				hostConn.CancelRead(errcode.PeerClosed)
				return nil
			}
			if errcode.IsRemoteStreamError(err, errcode.Cancelled) {
				hostConn.CancelRead(errcode.Cancelled)
				return nil
			}
			if errcode.IsLocalStreamError(err, errcode.HostClosed) {
				hostConn.CancelRead(errcode.HostClosed)
				return nil
			}

			hostConn.CancelRead(errcode.PeerInternalError)
			return fmt.Errorf("receive datagram: %w", err)
		}

		if err := hostConn.SendDatagram(dg); err != nil {
			peerConn.CancelRead(errcode.HostInternalError)
			return fmt.Errorf("send datagram: %w", err)
		}
	}
}

func (s *Registar) forwardToPeer(ctx context.Context, peerConn, hostConn http3.Stream) error {
	for {
		dg, err := hostConn.ReceiveDatagram(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				peerConn.CancelRead(errcode.Cancelled)
				return nil
			}
			if errcode.IsRemoteQUICConnClosed(err) {
				peerConn.CancelRead(errcode.HostClosed)
				return nil
			}
			if errcode.IsRemoteStreamError(err, errcode.Cancelled) {
				peerConn.CancelRead(errcode.Cancelled)
				return nil
			}
			// closed in another goroutine
			if errcode.IsLocalStreamError(err, errcode.PeerClosed) {
				peerConn.CancelRead(errcode.PeerClosed)
				return nil
			}

			peerConn.CancelRead(errcode.HostInternalError)
			return fmt.Errorf("receive datagram: %w", err)
		}

		if err := peerConn.SendDatagram(dg); err != nil {
			if errcode.IsRemoteQUICConnClosed(err) {
				peerConn.CancelRead(errcode.HostClosed)
				return nil
			}

			hostConn.CancelRead(errcode.PeerInternalError)
			return fmt.Errorf("send datagram: %w", err)
		}
	}
}

// Stop stops the service.
func (s *Registar) Stop() error {
	err := s.closeHosts()

	s.wg.Wait()
	return err
}

func (s *Registar) closeHosts() error {
	var err error
	var keys []auth.Key
	s.hosts.ForEach(func(key auth.Key, conn *control.Connector) {
		if e := conn.Close(); e != nil {
			err = fmt.Errorf("close connection: %w", e)
		}
		keys = append(keys, key)
	})

	for _, key := range keys {
		s.hosts.Delete(key, nil)
	}

	return err
}

// Peers returns all peers connected to the game.
func (s *Registar) Peers(key auth.Key) []string {
	return s.connectedPeers.Keys(key)
}

// NotifyPeerConnected adds a callback to be called when a peer connects.
func (s *Registar) NotifyPeerConnected(f func(auth.Key, string)) {
	s.connectedPeers.NotifyAdd(func(key auth.Key, peerID string, _ http3.Stream) {
		f(key, peerID)
	})
}

// NotifyPeerDisconnected adds a callback to be called when a peer disconnects.
func (s *Registar) NotifyPeerDisconnected(f func(auth.Key, string, error)) {
	s.connectedPeers.NotifyDelete(func(key auth.Key, peerID string, _ http3.Stream, reason error) {
		f(key, peerID, reason)
	})
}

// NotifyHostDisconnected adds a callback to be called when a host disconnects.
func (s *Registar) NotifyHostDisconnected(f func(auth.Key, error)) {
	s.hosts.NotifyDelete(func(key auth.Key, value *control.Connector, reason error) {
		f(key, reason)
	})
}
