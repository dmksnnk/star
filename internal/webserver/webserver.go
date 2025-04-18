package webserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"text/template"

	"github.com/dmksnnk/star/internal"
	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/host"
	"github.com/dmksnnk/star/internal/peer"
	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/registar/api"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/go-playground/form/v4"
)

var knownGames = GameCatalog{
	{
		ID:   "stardew-valley",
		Name: "Stardew Valley",
		Port: 24642,
	},
}

var knownURLs = []string{
	"https://localhost:8083",
}

var knownCACerts, _ = filepath.Glob("./*.crt")

type WebServer struct {
	tmpl *template.Template
	// inputCache is used to store the input data for the form
	// url -> key -> value
	inputCache map[string]url.Values
	closeApp   func()
	decoder    *form.Decoder
	logs       *platform.ChanLog
	logger     *slog.Logger

	wg       *sync.WaitGroup
	stopHost func() error
	stopPeer func() error
}

type Host interface {
	Start(ctx context.Context, req StartGameServerRequest)
}

func New(tmpl *template.Template, closeApp func()) *WebServer {
	logs := platform.NewChanLog()
	logger := slog.New(
		slog.NewTextHandler(io.MultiWriter(os.Stdout, logs), nil))

	return &WebServer{
		tmpl:       tmpl,
		inputCache: make(map[string]url.Values),
		closeApp:   closeApp,
		decoder:    newFormDecoder(),
		logs:       logs,
		logger:     logger,
		wg:         &sync.WaitGroup{},
	}
}

func (ws *WebServer) Index(w http.ResponseWriter, r *http.Request) {
	ws.executeTemplate(w, "index.html", nil)
}

func (ws *WebServer) StartHost(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data := StartGameServerPageData{
			Games: knownGames,
		}
		ws.executeTemplate(w, "start-host.html", &data)
		return
	}

	if err := r.ParseForm(); err != nil {
		ws.error(w, r, err)
		return
	}

	ws.inputCache["start-host.html"] = r.PostForm
	ws.startGameServer(w, r)
}

func (ws *WebServer) StartHostAdvanced(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data := StartGameServerAdvancedPageData{
			AdvancedPageData: AdvancedPageData{
				URLs:    knownURLs,
				CAFiles: knownCACerts,
			},
			Games: knownGames,
		}
		ws.executeTemplate(w, "start-host-advanced.html", &data)
		return
	}

	if err := r.ParseForm(); err != nil {
		ws.error(w, r, err)
		return
	}

	ws.inputCache["start-host-advanced.html"] = r.PostForm
	ws.startGameServer(w, r)
}

func (ws *WebServer) startGameServer(w http.ResponseWriter, r *http.Request) {
	var req StartGameServerRequest
	if err := ws.decoder.Decode(&req, r.PostForm); err != nil {
		ws.error(w, r, err)
		return
	}

	if err := req.Validate(); err != nil {
		data := StartGameServerPageData{
			Errors: []error{err},
			Games:  knownGames,
		}
		ws.executeTemplate(w, "start-host.html", &data)
		return
	}

	key := auth.NewKey()
	if err := ws.runHost(r.Context(), key, req); err != nil {
		ws.error(w, r, err)
		return
	}

	data := RunningServerPageData{
		GameAddress: ":" + strconv.Itoa(req.GamePort),
		InviteCode:  key.String(),
	}
	ws.executeTemplate(w, "running-host.html", &data)
}

func (ws *WebServer) runHost(ctx context.Context, key auth.Key, req StartGameServerRequest) error {
	dialer, err := internal.NewDialer(req.CACert)
	if err != nil {
		return fmt.Errorf("create dialer: %w", err)
	}

	u, err := url.Parse(req.URL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}

	client := api.NewClient(dialer, u, []byte(req.Secret))
	hostRegisterer := &host.Registerer{}
	hostForwarder, err := hostRegisterer.Register(ctx, client, key)
	if err != nil {
		return fmt.Errorf("register game: %w", err)
	}

	ws.stopHost = hostForwarder.Close

	ws.wg.Add(1)
	go func() {
		defer ws.wg.Done()

		ctx := context.Background()
		if err := hostForwarder.ListenAndForward(ctx, req.GamePort); err != nil {
			if errcode.IsLocalQUICConnClosed(err) {
				return
			}

			ws.logger.Error("listen and forward", "error", err)
			return
		}
	}()

	return nil
}

func (ws *WebServer) StopServer(w http.ResponseWriter, r *http.Request) {
	if ws.stopHost != nil {
		if err := ws.stopHost(); err != nil {
			ws.error(w, r, err)
			return
		}
	}

	ws.wg.Wait()
	ws.stopHost = nil

	data := StartGameServerPageData{
		Messages: []string{"Server stopped successfully"},
		Games:    knownGames,
	}
	ws.executeTemplate(w, "start-host.html", &data)
}

func (ws *WebServer) Connect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data := ConnectPageData{
			AdvancedPageData: AdvancedPageData{
				URLs:    knownURLs,
				CAFiles: knownCACerts,
			},
		}
		ws.executeTemplate(w, "connect.html", &data)
		return
	}

	if err := r.ParseForm(); err != nil {
		ws.error(w, r, err)
		return
	}

	ws.inputCache["connect.html"] = r.PostForm
	ws.connect(w, r)
}

func (ws *WebServer) connect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := ws.decoder.Decode(&req, r.PostForm); err != nil {
		ws.error(w, r, err)
		return
	}

	if err := req.Validate(); err != nil {
		data := ConnectPageData{
			AdvancedPageData: AdvancedPageData{
				URLs:    knownURLs,
				CAFiles: knownCACerts,
			},
			Errors: []error{err},
		}
		ws.executeTemplate(w, "connect.html", &data)
		return
	}

	addr, err := ws.runPeer(r.Context(), req)
	if err != nil {
		ws.error(w, r, err)
		return
	}

	data := ConnectedPageData{
		ListenAddress: addr.String(),
	}
	ws.executeTemplate(w, "connected.html", &data)
}

func (ws *WebServer) runPeer(ctx context.Context, req ConnectRequest) (net.Addr, error) {
	dialer, err := internal.NewDialer(req.CACert)
	if err != nil {
		return nil, fmt.Errorf("create dialer: %w", err)
	}

	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	client := api.NewClient(dialer, u, []byte(req.Secret))
	peerConn := &peer.Connector{}
	peerForwarder, err := peerConn.ConnectAndListen(ctx, client, req.InviteCode, req.Name)
	if err != nil {
		return nil, fmt.Errorf("connect and listen: %w", err)
	}

	ws.stopPeer = peerForwarder.Close

	ws.wg.Add(1)
	go func() {
		defer ws.wg.Done()
		if err := peerForwarder.AcceptAndForward(); err != nil {
			ws.logger.Error("accept and forward", "error", err)
			return
		}
	}()

	return peerForwarder.LocalAddr(), nil
}

// Disconnect stops the peer forwarder and waits for it to finish.
func (ws *WebServer) Disconnect(w http.ResponseWriter, r *http.Request) {
	if ws.stopPeer != nil {
		if err := ws.stopPeer(); err != nil {
			ws.error(w, r, err)
			return
		}
	}

	ws.wg.Wait()
	ws.stopPeer = nil

	data := ConnectPageData{
		Messages: []string{"Disconnected successfully"},
		AdvancedPageData: AdvancedPageData{
			URLs:    knownURLs,
			CAFiles: knownCACerts,
		},
	}
	ws.executeTemplate(w, "connect.html", &data)
}

func (ws *WebServer) Logs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx := r.Context()
	logs := ws.logs.Logs()
	for {
		select {
		case <-ctx.Done():
			return
		case log := <-logs:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", log)
			w.(http.Flusher).Flush()
		}
	}
}

func (ws *WebServer) Close(w http.ResponseWriter, r *http.Request) {
	ws.closeApp()
}

func (ws *WebServer) error(w http.ResponseWriter, r *http.Request, err error) {
	ws.logger.Error("error", "error", err)

	message := errorMessages[rand.Intn(len(errorMessages))]
	data := ErrorPageData{
		BackLink: r.Referer(),
		Error:    err.Error(),
		Message:  message,
	}

	ws.executeTemplate(w, "error.html", &data)
}

func (ws *WebServer) executeTemplate(w http.ResponseWriter, name string, data PageData) {
	if data != nil {
		data.SetCache(ws.inputCache[name])
	}

	if err := ws.tmpl.ExecuteTemplate(w, name, data); err != nil {
		ws.logger.Error("execute template", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
