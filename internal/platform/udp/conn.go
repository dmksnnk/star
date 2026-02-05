// Package udp provides a UDP-based implementation of net.Conn.
package udp

import (
	"net"
	"os"
	"sync"
	"time"
)

// Conn implements streaming [net.Conn] over a UDP.
type Conn struct {
	conn      net.PacketConn // underlying UDP connection
	reads     chan []byte    // channel for incoming data
	writeAddr net.Addr       // remote address for writes
	done      chan struct{}  // closed when connection is closed
	release   func() error   // cleanup function

	mu            sync.RWMutex // protects deadline fields
	readDeadline  time.Time    // deadline for Read
	writeDeadline time.Time    // deadline for Write
}

var _ net.Conn = &Conn{}

// LocalAddr returns the local network address.
func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// RemoteAddr returns the remote network address to which the connection is bound.
func (c *Conn) RemoteAddr() net.Addr {
	return c.writeAddr
}

// SetReadDeadline sets the deadline for future Read calls.
// A zero value for t means Read will not time out.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readDeadline = t
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls.
// A zero value for t means Write will not time out.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.writeDeadline = t
	return nil
}

// SetDeadline sets the read and write deadlines.
// A zero value for t means I/O operations will not time out.
func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readDeadline = t
	c.writeDeadline = t
	return nil
}

// Read reads data from the connection into p.
// It blocks until data is available, the deadline is exceeded, or the connection is closed.
func (c *Conn) Read(p []byte) (int, error) {
	c.mu.RLock()
	dl, cancel := newDeadline(c.readDeadline)
	defer cancel()
	c.mu.RUnlock()

	select {
	case <-dl.Done():
		return 0, os.ErrDeadlineExceeded
	case data := <-c.reads:
		return copy(p, data), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

// Write writes data to the remote address.
// It blocks until the data is sent, the deadline is exceeded, or the connection is closed.
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.RLock()
	dl, cancel := newDeadline(c.writeDeadline)
	defer cancel()
	c.mu.RUnlock()

	select {
	case <-dl.Done():
		return 0, os.ErrDeadlineExceeded
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}

	return c.conn.WriteTo(p, c.writeAddr)
}

// Close terminates the connection and unblocks any pending [Conn.Read] calls.
// Since this is a UDP connection, the remote side will not receive a FIN signal,
// so its [net.Conn.Read] method will not return EOF and will continue to block.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	select {
	case <-c.done:
		return nil
	default:
		close(c.done)

		return c.release()
	}
}
