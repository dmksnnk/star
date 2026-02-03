package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/quic-go/quic-go"
	"golang.org/x/sync/errgroup"
)

const StatusCodeConnectFailed = http.StatusPreconditionFailed

// Agent listens for incoming requests from [Controller].
type Agent struct {
	mux *http.ServeMux
}

func NewAgent() *Agent {
	return &Agent{
		mux: http.NewServeMux(),
	}
}

// OnConnectTo registers a callback on command to connect to another peer.
// The callback should return true if connection attempt was started successfully.
func (a *Agent) OnConnectTo(f func(ctx context.Context, cmd ConnectCommand) (bool, error)) {
	a.mux.HandleFunc("POST /connect-to", func(w http.ResponseWriter, r *http.Request) {
		var cmd ConnectCommand
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ok, err := f(r.Context(), cmd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if !ok {
			w.WriteHeader(StatusCodeConnectFailed)
			return
		}

		w.WriteHeader(http.StatusOK)
	})
}

// Serve serves the connection.
// To stop the Agent, connection should be closed.
func (a *Agent) Serve(conn *quic.Conn) error {
	eg, ctx := errgroup.WithContext(conn.Context())
	eg.Go(func() error {
		// Accept connection until server is closed.
		// If connection gets overtaken, it will only open new streams,
		// so it is OK to keep accepting for other streams.
		for {
			str, err := conn.AcceptStream(ctx)
			if err != nil { // server context closed or error
				return fmt.Errorf("accept stream: %w", err)
			}

			eg.Go(func() error {
				return a.handleStream(str)
			})
		}
	})

	return eg.Wait()
}

// handleStream handles a single QUIC stream by reading HTTP request and writing response.
func (a *Agent) handleStream(stream *quic.Stream) error {
	reader := bufio.NewReader(stream)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}

	// req = req.WithContext(stream.Context())

	var rw responseWriter
	a.mux.ServeHTTP(&rw, req)
	req.Body.Close()
	if err := rw.write(stream); err != nil {
		return fmt.Errorf("write response: %w", err)
	}

	return stream.Close() // close send direction, so that client knows response is complete
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
