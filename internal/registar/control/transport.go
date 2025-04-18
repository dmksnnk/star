package control

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// transport is simple transport on a top of a single connection.
type transport struct {
	mux  sync.Mutex
	conn net.Conn
	br   *bufio.Reader
}

func newTransport(conn net.Conn) *transport {
	return &transport{
		conn: conn,
		br:   bufio.NewReader(conn),
	}
}

// RoundTrip implements http.RoundTripper interface.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mux.Lock() // allow only one request/response at a time
	defer t.mux.Unlock()

	ctx := req.Context()
	if dl, ok := ctx.Deadline(); ok {
		if err := t.conn.SetDeadline(dl); err != nil {
			return nil, fmt.Errorf("set deadline: %w", err)
		}
		defer func() {
			_ = t.conn.SetDeadline(time.Time{})
		}()
	}

	if err := req.Write(t.conn); err != nil {
		return nil, err
	}

	return http.ReadResponse(t.br, req)
}

// Handle handles a single HTTP request.
func (t *transport) Handle(handler func(*http.Request) *http.Response) error {
	req, err := http.ReadRequest(t.br)
	if err != nil {
		return err
	}

	resp := handler(req)
	if resp == nil {
		resp = &http.Response{
			StatusCode: http.StatusOK,
		}
	}

	return resp.Write(t.conn)
}

// Close closes the transport.
func (t *transport) Close() error {
	return t.conn.Close()
}
