package control

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
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
	})
}

// Serve serves the connection.
// To stop the Agent, connection should be closed.
func (a *Agent) Serve(conn *quic.Conn) error {
	srv := &http3.Server{
		Handler: a.mux,
	}

	return srv.ServeQUICConn(conn)
}
