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
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

var errCancelled = errors.New("relay: cancelled")

// Stats contains UDP relay statistics.
type Stats struct {
	BytesIn        uint64
	BytesOut       uint64
	DroppedPackets uint64
}

type UDPRelay struct {
	bufPool sync.Pool
	workers int

	routesMu sync.RWMutex
	routes   map[netip.AddrPort]netip.AddrPort

	done chan struct{}

	logger         *slog.Logger
	bytesIn        atomic.Uint64
	bytesOut       atomic.Uint64
	droppedPackets atomic.Uint64
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
		routes:  make(map[netip.AddrPort]netip.AddrPort),
		done:    make(chan struct{}),
		workers: runtime.NumCPU(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	for _, opt := range opts {
		opt(&r)
	}

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

func (r *UDPRelay) ListenUDP(addr *net.UDPAddr) error {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	return r.Serve(conn)
}

// AddRoute adds a bidirectional route between addresses a and b.
func (r *UDPRelay) AddRoute(a, b netip.AddrPort) {
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	r.routes[a] = b
	r.routes[b] = a
}

func (r *UDPRelay) RemoveRoute(a, b netip.AddrPort) {
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	delete(r.routes, a)
	delete(r.routes, b)
}

// RemoveAllRoutes removes all routes associated with address a.
func (r *UDPRelay) RemoveAllRoutes(a netip.AddrPort) {
	r.routesMu.Lock()
	defer r.routesMu.Unlock()

	delete(r.routes, a)
	for _, dst := range r.routes {
		if dst == a {
			delete(r.routes, dst)
		}
	}
}

// Stats returns current relay statistics.
func (r *UDPRelay) Stats() Stats {
	return Stats{
		BytesIn:        r.bytesIn.Load(),
		BytesOut:       r.bytesOut.Load(),
		DroppedPackets: r.droppedPackets.Load(),
	}
}

// Close sends a signal to stop all relay operations.
// It will stop reading new packets, but will process all packets already read.
// For proper shutdown, ensure that all [UDPRelay.Serve] calls have returned after invoking Close.
func (r *UDPRelay) Close() error {
	close(r.done)

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
	defer func() {
		_ = conn.SetDeadline(time.Time{}) // clear deadline
	}()

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

	r.routesMu.RLock()
	dst, ok := r.routes[pkt.addr]
	r.routesMu.RUnlock()
	if !ok { // drop packet if no route
		r.logger.Warn("dropping packet with no route", slog.String("addr", pkt.addr.String()), slog.Int("size", pkt.n))
		r.droppedPackets.Add(1)
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
