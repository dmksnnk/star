package registar

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

const (
	defaultConnectTimeout = time.Minute
	defaultQueueSize      = 10
)

type AddrPair struct {
	Public  netip.AddrPort
	Private netip.AddrPort
}

type Session struct {
	host Peer

	wg    sync.WaitGroup
	mux   sync.Mutex
	peers map[Peer]struct{}

	notifyDisconnected []func(AddrPair, error)
}

func NewSession(addrs AddrPair, controlStream *http3.Stream) *Session {
	return &Session{
		host:  NewPeer(addrs, controlStream),
		peers: make(map[Peer]struct{}),
	}
}

// Join returns true if p2p connection is established, false if via relay.
func (s *Session) Join(
	ctx context.Context,
	addrs AddrPair,
	controlStream *http3.Stream,
) error {
	peer := NewPeer(addrs, controlStream)

	if err := initP2P(ctx, peer, s.host); err != nil {
		// return false, s.initRelayConnection(ctx, peerCtrl)
		return fmt.Errorf("init P2P connection: %w", err)
	}

	s.mux.Lock()
	defer s.mux.Unlock()
	s.peers[peer] = struct{}{}
	fmt.Println("session: adding peer")

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ctx := controlStream.Context()
		<-ctx.Done()

		s.mux.Lock()
		delete(s.peers, peer)
		s.mux.Unlock()

		for _, notify := range s.notifyDisconnected {
			notify(addrs, context.Cause(ctx))
		}
	}()

	return nil
}

func (s *Session) OnPeerDisconnected(f func(AddrPair, error)) {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.notifyDisconnected = append(s.notifyDisconnected, f)
}

func (s *Session) Close() error {
	s.mux.Lock()
	for p := range s.peers {
		p.Cancel(errcode.Cancelled)
		delete(s.peers, p)
	}
	s.mux.Unlock()

	s.host.Cancel(errcode.Cancelled)

	s.wg.Wait()

	return nil
}

// func (s *Session) initRelayConnection(ctx context.Context, peerCtrl *control.Controller) error {
// 	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
// 	defer cancel()

// 	// this will return error is join via relay fails
// 	return peerCtrl.JoinViaRelay(ctx, peerID)
// }

func initP2P(ctx context.Context, host, peer Peer) error {
	ctx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return host.control().ConnectTo(ctx, peer.addrs.Public, peer.addrs.Private)
	})
	eg.Go(func() error {
		return peer.control().ConnectTo(ctx, host.addrs.Public, host.addrs.Private)
	})

	return eg.Wait()
}

// // JoinRelay puts peer into the waiting queue and asks host to initialize new stream for relaying traffic.
// func (s *Session) JoinRelay(ctx context.Context, peerID string, peerStream *http3.Stream) error {
// 	w := newWaiter(peerStream)
// 	select {
// 	case s.queue <- w: // FIXME: maybe just use map for waiting peers, it is sequential access anyway
// 	case <-ctx.Done():
// 		return ctx.Err()
// 	}

// 	if err := s.host.control.InitRelayStream(ctx, peerID); err != nil {
// 		return fmt.Errorf("request host relay peer: %w", err)
// 	}

// 	return w.Wait(ctx)
// }

// // RelayPeer takes a waiting peer from the queue and relays traffic from the host to the peer.
// func (s *Session) RelayPeer(ctx context.Context, peerID string, hostStream *http3.Stream) error {
// 	var peerWaiter Waiter[*http3.Stream]
// 	select {
// 	case peerWaiter = <-s.queue:
// 	case <-ctx.Done():
// 		return ctx.Err()
// 	}

// 	defer peerWaiter.Done()

// 	fwdCtx, cancel := context.WithCancel(context.Background())
// 	s.wg.Add(1)
// 	go func() {
// 		defer s.wg.Done()
// 		err := Forward(fwdCtx, hostStream, peerWaiter.Value())
// 		for _, notify := range s.notifyDisconnected {
// 			notify(peerID, err)
// 		}
// 	}()

// 	s.mux.Lock()
// 	defer s.mux.Unlock()
// 	s.peers[peerID] = cancel

// 	return nil
// }

type Peer struct {
	addrs         AddrPair
	controlStream *http3.Stream
}

func NewPeer(addrs AddrPair, controlStream *http3.Stream) Peer {
	return Peer{
		addrs:         addrs,
		controlStream: controlStream,
	}
}

func (p Peer) control() *control.Controller {
	return control.NewController(p.controlStream)
}

func (p Peer) Cancel(code quic.StreamErrorCode) {
	p.controlStream.CancelRead(code)
	p.controlStream.CancelWrite(code)
}
