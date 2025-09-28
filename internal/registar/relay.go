package registar

import (
	"context"
	"errors"
	"fmt"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

const bufSize = 64 * 1024

// type waitingID struct {
// 	peerID string
// 	key    auth.Key
// }

// type Relay struct {
// 	mux     sync.Mutex
// 	waiting map[waitingID]*http3.Stream

// 	links map[*link]struct{}
// }

// // TODO: add notify when peer disconnects

// func (r *Relay) AddWaiting(key auth.Key, peerID string, stream *http3.Stream) error {
// 	r.mux.Lock()
// 	defer r.mux.Unlock()

// 	go func() {
// 		// TODO: wait for stream to be closed and then remove from waiting
// 		// or remove from waiting when link is established
// 		// e.g. create tracking of waiting streams
// 	}()

// 	id := waitingID{key: key, peerID: peerID}
// 	if _, ok := r.waiting[id]; ok {
// 		r.mux.Unlock()
// 		return fmt.Errorf("peer %q is already waiting for key %s", peerID, key)
// 	}

// 	r.waiting[id] = stream
// 	return nil
// }

// func (r *Relay) Relay(ctx context.Context, key auth.Key, peerID string, hostStream *http3.Stream) error {
// 	r.mux.Lock()
// 	defer r.mux.Unlock()

// 	stream, ok := r.waiting[waitingID{key: key, peerID: peerID}]
// 	if !ok {
// 		return fmt.Errorf("no waiting peer %q for key %s", peerID, key)
// 	}
// 	// TODO: notify tracking of waiting stream, so we can remove it from waiting list

// 	delete(r.waiting, waitingID{key: key, peerID: peerID})

// 	// TODO: start forwarding, create link, add the to r.links
// }

// func (r *Relay) Close() error {
// 	r.mux.Lock()
// 	defer r.mux.Unlock()

// 	for l := range r.links {
// 		l.close()
// 	}
// 	return nil
// }

func Forward(ctx context.Context, hostStream, peerStream *http3.Stream) error {
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return forwardDatagramsToHost(ctx, hostStream, peerStream)
	})
	eg.Go(func() error {
		return forwardDatagramsToPeer(ctx, peerStream, hostStream)
	})

	return eg.Wait()
}

func forwardDatagramsToHost(ctx context.Context, hostConn, peerConn *http3.Stream) error {
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
			return fmt.Errorf("receive datagram from peer: %w", err)
		}

		if err := hostConn.SendDatagram(dg); err != nil {
			peerConn.CancelRead(errcode.HostInternalError)
			return fmt.Errorf("send datagram to host: %w", err)
		}
	}
}

func forwardDatagramsToPeer(ctx context.Context, peerConn, hostConn *http3.Stream) error {
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
			return fmt.Errorf("receive datagram from host: %w", err)
		}

		if err := peerConn.SendDatagram(dg); err != nil {
			if errcode.IsRemoteQUICConnClosed(err) {
				peerConn.CancelRead(errcode.HostClosed)
				return nil
			}

			hostConn.CancelRead(errcode.PeerInternalError)
			return fmt.Errorf("send datagram to peer: %w", err)
		}
	}
}
