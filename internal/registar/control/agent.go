package control

import (
	"encoding/json"
	"io"
	"net/http"
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

// OnRequestForward registers a callback on command to forward connection from Registar.
func (a *Agent) OnRequestForward(f func(RequestForwardCommand) error) {
	a.mux.HandleFunc("POST /forward", func(w http.ResponseWriter, r *http.Request) {
		var cmd RequestForwardCommand
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if err := f(cmd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

// OnConnectTo registers a callback on command to connect to another peer.
func (a *Agent) OnConnectTo(f func(ConnectCommand) (bool, error)) {
	a.mux.HandleFunc("POST /connect-to", func(w http.ResponseWriter, r *http.Request) {
		var cmd ConnectCommand
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ok, err := f(cmd)
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
func (a *Agent) Serve(conn io.ReadWriter) error {
	srv := newServer(a.mux)
	return srv.Serve(conn)
}
