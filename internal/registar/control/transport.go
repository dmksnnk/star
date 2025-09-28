package control

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// transport is simple transport on a top of a single connection.
type transport struct {
	mux    sync.Mutex
	writer io.Writer
	br     *bufio.Reader
}

func newTransport(conn io.ReadWriter) *transport {
	return &transport{
		writer: conn,
		br:     bufio.NewReader(conn),
	}
}

// RoundTrip implements http.RoundTripper interface.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mux.Lock()         // allow only one request/response at a time
	defer t.mux.Unlock() // FIXME: do we need this?

	if err := req.Write(t.writer); err != nil {
		return nil, err
	}

	return http.ReadResponse(t.br, req)
}

type server struct {
	handler http.Handler
}

func newServer(handler http.Handler) *server {
	return &server{
		handler: handler,
	}
}

// Serve serves HTTP requests over single connection.
// One each request appropriate handler will be called.
// To stop the server, connection should be closed.
func (s *server) Serve(conn io.ReadWriter) error {
	br := bufio.NewReader(conn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return err
		}

		var rw responseWriter
		s.handler.ServeHTTP(&rw, req)
		req.Body.Close()

		if err := rw.write(conn); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
}

type responseWriter struct {
	status int
	header http.Header
	body   bytes.Buffer
}

// Header returns the header map that will be sent in response.
func (rw *responseWriter) Header() http.Header {
	if rw.header == nil {
		rw.header = make(http.Header)
	}
	return rw.header
}

// Write writes the data to the connection as part of an HTTP reply.
func (rw *responseWriter) Write(b []byte) (int, error) {
	return rw.body.Write(b)
}

// WriteHeader sets an HTTP response header.
func (rw *responseWriter) WriteHeader(statusCode int) {
	rw.status = statusCode
}

func (rw *responseWriter) write(w io.Writer) error {
	resp := &http.Response{
		StatusCode: rw.status,
		Body:       io.NopCloser(&rw.body),
		Header:     rw.header,
	}
	if resp.StatusCode == 0 {
		resp.StatusCode = http.StatusOK
	}
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}

	return resp.Write(w)
}
