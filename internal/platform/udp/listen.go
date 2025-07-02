package udp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/dmksnnk/star/internal/platform"
)

var (
	ErrClosedListener      = errors.New("udp: listener closed")
	ErrListenQueueExceeded = errors.New("udp: listen queue exceeded")
	ErrReadBufferExceeded  = errors.New("upd: read buffer size exceeded")
)

const (
	defaultConnBacklog    = 128
	defaultReadBufferSize = 512
)

// ListenConfig implements stream-oriented connection on the top of UDP.
type ListenConfig struct {
	// ListenConfig is a configuration for listening on a UDP address.
	ListenConfig net.ListenConfig
	// ConnBacklog is a maximum number of connections waiting for accepting.
	// Default value is 128.
	// If the backlog is exceeded, ErrListenQueueExceeded error will be returned.
	ConnBacklog int
	// ReadBufferSize is a size of a buffer for read packets on a single connection.
	// If nothing is reading from the connection and buffer is full,
	// packets will be dropped with ErrReadBufferExceeded error.
	ReadBufferSize int
	// Logger is used to log errors on listener.
	// If nil, [slog.Default] will be used.
	Logger *slog.Logger
}

// Listen creates a new UDP listener with the specified address.
// The ctx argument is used while resolving the address on which to listen;
// it does not affect the returned Listener.
func (l ListenConfig) Listen(ctx context.Context, addr *net.UDPAddr) (net.Listener, error) {
	conn, err := l.ListenConfig.ListenPacket(ctx, addr.Network(), addr.String())
	if err != nil {
		return nil, err
	}

	connBacklog := l.ConnBacklog
	if connBacklog <= 0 {
		connBacklog = defaultConnBacklog
	}
	readBufferSize := l.ReadBufferSize
	if readBufferSize <= 0 {
		readBufferSize = defaultReadBufferSize
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}

	lis := &Listener{
		conn:           conn,
		acceptQueue:    make(chan *Conn, connBacklog),
		done:           make(chan struct{}),
		readBufferSize: readBufferSize,
		readDone:       make(chan struct{}),
		conns:          make(map[string]*Conn),
		logger:         logger,
	}
	go lis.readLoop()

	return lis, nil
}

// Listener listens for incoming packets.
// Implements [net.Listener] interface
type Listener struct {
	conn        net.PacketConn
	acceptQueue chan *Conn
	done        chan struct{}

	readBufferSize int
	readDone       chan struct{}
	readErr        atomic.Value

	conns    map[string]*Conn
	connLock sync.RWMutex

	logger *slog.Logger
}

var _ net.Listener = &Listener{}

func (l *Listener) readLoop() {
	defer close(l.readDone)

	for {
		buf := make([]byte, platform.MTU)
		n, addr, err := l.conn.ReadFrom(buf)
		if err != nil {
			l.readErr.Store(err)
			return
		}

		if err := l.dispatch(addr, buf[:n]); err != nil {
			l.logger.Error("can't dispatch packet", "error", err, "address", addr.String())
		}
	}
}

func (l *Listener) dispatch(addr net.Addr, packet []byte) error {
	l.connLock.RLock()
	conn, ok := l.conns[addr.String()]
	l.connLock.RUnlock()

	if ok {
		select {
		case conn.reads <- packet:
			return nil
		case <-l.done:
			return ErrClosedListener
		default:
			return ErrReadBufferExceeded
		}
	}

	// conn not found, create new one
	conn = &Conn{
		conn:      l.conn,
		reads:     make(chan []byte, l.readBufferSize),
		writeAddr: addr,
		done:      make(chan struct{}),
		release: func() error {
			l.connLock.Lock()
			defer l.connLock.Unlock()

			// remove connection from the map
			delete(l.conns, addr.String())

			return l.tryCloseConn()
		},
	}
	conn.reads <- packet // add initial data

	select {
	case l.acceptQueue <- conn:
		// store connection, even if it is not accepted yet,
		// so when new packet arrives we know where to send packets
		l.connLock.Lock()
		l.conns[addr.String()] = conn
		l.connLock.Unlock()
		return nil
	case <-l.done:
		return ErrClosedListener
	default:
		return ErrListenQueueExceeded
	}
}

// Accept waits for and returns the next connection to the listener.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.acceptQueue:
		return conn, nil
	case <-l.readDone:
		return nil, l.readErr.Load().(error)
	case <-l.done:
		return nil, ErrClosedListener
	}
}

// Addr returns the listener's network address.
func (l *Listener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

// Close closes the listener.
// Any blocked [Listener.Accept] operations will be unblocked and return ErrClosedListener.
// But, it will still process incoming reads as connections until there are no connections left.
func (l *Listener) Close() error {
	// lock to not add new connections and avoid races on multiple calls to close
	l.connLock.Lock()
	defer l.connLock.Unlock()

	select {
	case <-l.done:
		return nil
	default:
		close(l.done)

		return l.tryCloseConn()
	}
}

// tryCloseConn tries to close net.PacketConn when listener is closed and there are no connections left.
func (l *Listener) tryCloseConn() error {
	select {
	case <-l.done:
		if len(l.conns) == 0 {
			err := l.conn.Close()
			<-l.readDone
			return err
		}

		return nil
	default:
		return nil
	}
}
