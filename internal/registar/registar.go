package registar

import (
	"context"
	"fmt"
	"sync"

	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

type Registar2 struct {
	wg       sync.WaitGroup
	mux      sync.RWMutex
	sessions map[auth.Key]*Session

	notifySessionDone []func(auth.Key, error)
	notifyJoined      []func(auth.Key, AddrPair)

	// relay *Relay
}

var _ Registar = (*Registar2)(nil)

func NewRegistar2() *Registar2 {
	return &Registar2{
		sessions: make(map[auth.Key]*Session),
		// TODO: create relay
	}
}

func (r *Registar2) Host(
	ctx context.Context,
	key auth.Key,
	streamer http3.HTTPStreamer,
	addrs AddrPair,
) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	if _, ok := r.sessions[key]; ok {
		return ErrHostAlreadyExists
	}
	stream := streamer.HTTPStream()

	sess := NewSession(addrs, stream)
	r.sessions[key] = sess

	// start monitoring host disconnect
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()

		ctx := stream.Context()
		<-ctx.Done()

		r.mux.Lock()
		sess.Close()
		delete(r.sessions, key)
		r.mux.Unlock()

		for _, notify := range r.notifySessionDone {
			notify(key, context.Cause(ctx))
		}
	}()

	return nil
}

func (r *Registar2) Join(
	ctx context.Context,
	key auth.Key,
	streamer http3.HTTPStreamer,
	addrs AddrPair,
) error {
	sess, ok := r.Session(key)
	if !ok {
		return ErrHostNotFound
	}

	if err := sess.Join(ctx, addrs, streamer.HTTPStream()); err != nil {
		return fmt.Errorf("join session: %w", err)
	}

	for _, notify := range r.notifyJoined {
		notify(key, addrs)
	}

	return nil
}

func (r *Registar2) OnSessionDone(f func(auth.Key, error)) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.notifySessionDone = append(r.notifySessionDone, f)
}

func (r *Registar2) OnJoined(f func(auth.Key, AddrPair)) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.notifyJoined = append(r.notifyJoined, f)
}

func (r *Registar2) Session(key auth.Key) (*Session, bool) {
	r.mux.RLock()
	defer r.mux.RUnlock()
	sess, ok := r.sessions[key]

	return sess, ok
}

func (r *Registar2) Close() error {
	r.mux.Lock()
	for k, sess := range r.sessions {
		sess.Close()
		for _, notify := range r.notifySessionDone {
			notify(k, nil)
		}
		delete(r.sessions, k)
	}

	defer r.mux.Unlock()

	r.wg.Wait()
	return nil
}

// // JoinRelay joins a peer into session via relay.
// func (r *Registar2) JoinRelay(ctx context.Context, key auth.Key, peerID string, stream *http3.Stream) error {
// 	return r.relay.AddWaiting(key, peerID, stream)

// 	// sess, ok := r.session(key)
// 	// if !ok {
// 	// 	return ErrHostNotFound
// 	// }

// 	// if err := sess.JoinRelay(context.Background(), peerID, stream); err != nil {
// 	// 	return fmt.Errorf("join via relay: %w", err)
// 	// }

// 	// return nil
// }

// // RelayPeer connects a peer to host via relay.
// func (r *Registar2) RelayPeer(ctx context.Context, key auth.Key, peerID string, stream *http3.Stream) error {
// 	return r.relay.Relay(ctx, key, peerID, stream)

// 	// sess, ok := r.session(key)
// 	// if !ok {
// 	// 	return ErrHostNotFound
// 	// }

// 	// if err := sess.RelayPeer(ctx, peerID, stream); err != nil {
// 	// 	return fmt.Errorf("relay peer: %w", err)
// 	// }

// 	// return nil
// }
