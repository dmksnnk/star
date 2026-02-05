package udp

import (
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
	// ConnBacklog is the maximum number of connections waiting to be accepted.
	// The default value is 128.
	// If the backlog is exceeded, [ErrListenQueueExceeded] will be returned.
	ConnBacklog int
	// ReadBufferSize is the size of the buffer for reading packets on a single connection.
	// If nothing is reading from the connection and the buffer is full,
	// packets will be dropped with [ErrReadBufferExceeded].
	// This ensures that one blocked reader doesn't block accepting new connections
	// or reading from other connections.
	ReadBufferSize int
	// Logger is used to log errors on the listener.
	// If nil, [slog.Default] will be used.
	Logger *slog.Logger
}

// Listen creates a new UDP listener with the specified address.
//
// If the IP field of addr is nil or an unspecified IP address,
// Listen listens on all available IP addresses of the local system
// except multicast IP addresses.
// If the Port field of addr is 0, a port number is automatically
// chosen.
func (l ListenConfig) Listen(addr *net.UDPAddr) (*Listener, error) {
	conn, err := net.ListenUDP("udp", addr)
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

// Listen is a convenience function to create a UDP listener with default configuration.
// See [ListenConfig] for more details.
func Listen(addr *net.UDPAddr) (*Listener, error) {
	return ListenConfig{}.Listen(addr)
}

// Listener listens for incoming packets.
// Implements [net.Listener] interface
type Listener struct {
	conn        *net.UDPConn
	acceptQueue chan *Conn
	done        chan struct{}

	readBufferSize int
	readDone       chan struct{} // closed when readLoop exits with error
	readErr        atomic.Value

	conns    map[string]*Conn
	connLock sync.RWMutex

	logger *slog.Logger
}

var _ net.Listener = &Listener{}

func (l *Listener) readLoop() {
	defer close(l.readDone)

	for {
		buf := make([]byte, platform.MTU) // TODO: use sync.Pool
		n, addr, err := l.conn.ReadFrom(buf)
		if err != nil {
			l.readErr.Store(err)
			return
		}

		if err := l.dispatch(addr, buf[:n]); err != nil {
			l.logger.Error("can't dispatch packet", "error", err, "addr", addr.String())
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
		default:
			return ErrReadBufferExceeded
		}
	}

	select {
	case <-l.done:
		return ErrClosedListener
	default:
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
// Returns [ErrClosedListener] when listener is closed.
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

// UDPAddr returns the listener's network address as *net.UDPAddr.
func (l *Listener) UDPAddr() *net.UDPAddr {
	return l.conn.LocalAddr().(*net.UDPAddr)
}

// Close closes the listener.
// Any blocked [Listener.Accept] operations will be unblocked and return ErrClosedListener.
// Already accepted connections are not closed.
// Underlying [net.PacketConn] is closed when there are no accepted connections left.
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
