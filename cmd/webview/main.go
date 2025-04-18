package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"text/template"
	"time"

	"github.com/dmksnnk/star/internal/webserver"
	"github.com/dmksnnk/star/web"
	webview "github.com/webview/webview_go"
	"golang.org/x/exp/slog"
	"golang.org/x/sync/errgroup"
)

func main() {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		slog.Error("listen on TCP", "error", err)
		os.Exit(1)
	}

	tmpl, err := template.ParseFS(web.Templates, "templates/*.html")
	if err != nil {
		slog.Error("parse templates", "error", err)
		os.Exit(1)
	}

	w := webview.New(true)
	defer w.Destroy()

	ws := webserver.New(tmpl, w.Terminate)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServerFS(web.Static))
	webserver.AddRoutes(mux, ws)

	srv := http.Server{
		Handler: mux,
	}

	var eg errgroup.Group
	eg.Go(func() error {
		if err := srv.Serve(listener); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}

		return nil
	})

	slog.Info("server started", "addr", listener.Addr().String())

	w.SetTitle("Star")
	w.SetSize(480, 600, webview.HintNone)

	u := url.URL{
		Scheme: "http",
		Host:   listener.Addr().String(),
	}
	w.Navigate(u.String())

	w.Run()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown server", "error", err)
	}
	if err := eg.Wait(); err != nil {
		slog.Error("wait for server", "error", err)
		os.Exit(1)
	}
}
