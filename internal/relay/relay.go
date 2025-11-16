package relay

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

var errCancelled = errors.New("relay: cancelled")

type UDPRelay struct {
	bufPool sync.Pool
	workers int

	routes *TTLMap[netip.AddrPort, netip.AddrPort]

	done chan struct{}

	logger              *slog.Logger
	rateLoggingInterval time.Duration
	bytesIn             atomic.Uint64
	bytesOut            atomic.Uint64
}

type packet struct {
	n    int
	bufp *[]byte
	addr netip.AddrPort
}

func NewUDPRelay(opts ...Option) *UDPRelay {
	r := UDPRelay{
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, 1500) // safe UDP packet size
				return &buf
			},
		},
		routes:              NewTTLMap[netip.AddrPort, netip.AddrPort](30 * time.Minute),
		done:                make(chan struct{}),
		workers:             runtime.NumCPU(),
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		rateLoggingInterval: 10 * time.Second,
	}

	for _, opt := range opts {
		opt(&r)
	}

	go r.logStats()

	return &r
}

func (r *UDPRelay) Serve(conn *net.UDPConn) error {
	inbox := make(chan packet, r.workers)

	var eg errgroup.Group
	eg.Go(func() error {
		defer close(inbox)

		// read in one goroutine, because it is blocked waiting for packets
		return r.readPackets(conn, inbox)
	})

	for range r.workers {
		eg.Go(func() error {
			for {
				select {
				case <-r.done:
					for pkr := range inbox { // drain inbox
						if err := r.writePacket(conn, pkr); err != nil {
							return fmt.Errorf("write packet: %w", err)
						}
					}
					return nil
				case pkt, ok := <-inbox:
					if !ok {
						return nil
					}
					if err := r.writePacket(conn, pkt); err != nil {
						return fmt.Errorf("write packet: %w", err)
					}
				}
			}
		})
	}

	return eg.Wait()
}

// AddRoute adds a bidirectional route between addresses a and b.
func (r *UDPRelay) AddRoute(a, b netip.AddrPort) {
	r.routes.Set(a, b)
	r.routes.Set(b, a)
}

// Close sends a signal to stop all relay operations.
// It will stop reading new packets, but will process all packets already read.
// For proper shutdown, ensure that all [UDPRelay.Serve] calls have returned after invoking Close.
func (r *UDPRelay) Close() error {
	close(r.done)
	r.routes.Close()

	return nil
}

func (r *UDPRelay) readPackets(conn *net.UDPConn, inbox chan<- packet) error {
	for {
		bufp := r.bufPool.Get().(*[]byte)
		pkt, err := r.readPacketWithTimeout(conn, bufp)
		if err != nil {
			r.bufPool.Put(bufp)

			if errors.Is(err, errCancelled) {
				return nil
			}

			return fmt.Errorf("read packet with timeout: %w", err)
		}

		r.bytesIn.Add(uint64(pkt.n))

		select {
		case <-r.done:
			r.bufPool.Put(bufp)
			return nil
		case inbox <- pkt:
		}
	}
}

func (r *UDPRelay) readPacketWithTimeout(conn *net.UDPConn, bufp *[]byte) (packet, error) {
	for {
		select {
		case <-r.done:
			return packet{}, errCancelled
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return packet{}, fmt.Errorf("set read deadline: %w", err)
		}

		n, addr, err := conn.ReadFromUDPAddrPort(*bufp)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			conn.SetDeadline(time.Time{}) // clear deadline

			return packet{}, fmt.Errorf("read from udp: %w", err)
		}

		return packet{
			n:    n,
			bufp: bufp,
			addr: addr,
		}, nil
	}
}

func (r *UDPRelay) writePacket(conn *net.UDPConn, pkt packet) error {
	defer r.bufPool.Put(pkt.bufp)

	dst, ok := r.routes.Get(pkt.addr)
	if !ok { // drop packet if no route
		r.logger.Warn("dropping packet with no route", slog.String("addr", pkt.addr.String()), slog.Int("size", pkt.n))
		return nil
	}

	n, err := conn.WriteToUDPAddrPort((*pkt.bufp)[:pkt.n], dst)
	if err != nil {
		return fmt.Errorf("write to udp: %w", err)
	}
	if n != pkt.n {
		return fmt.Errorf("short write to udp: wrote %d bytes, expected %d", n, pkt.n)
	}

	r.bytesOut.Add(uint64(n))

	return nil
}

func (r *UDPRelay) logStats() error {
	ticker := time.NewTicker(r.rateLoggingInterval)
	defer ticker.Stop()

	var lastIn, lastOut uint64
	lastTime := time.Now()

	for {
		select {
		case <-r.done:
			return nil
		case now := <-ticker.C:
			interval := now.Sub(lastTime).Seconds()
			lastTime = now

			in := r.bytesIn.Load()
			out := r.bytesOut.Load()

			inRate := float64(in-lastIn) / interval / 1024
			outRate := float64(out-lastOut) / interval / 1024

			lastIn = in
			lastOut = out

			r.logger.Info("stats",
				slog.Group("kibps",
					slog.String("in", strconv.FormatFloat(inRate, 'f', 2, 64)),
					slog.String("out", strconv.FormatFloat(outRate, 'f', 2, 64)),
				),
			)
		}
	}
}
