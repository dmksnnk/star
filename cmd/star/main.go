package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmksnnk/star/internal"
	"github.com/dmksnnk/star/internal/host"
	"github.com/dmksnnk/star/internal/peer"
	"github.com/dmksnnk/star/internal/registar/api"
	"github.com/dmksnnk/star/internal/registar/auth"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx, close := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer close()

	var cfg commandConfig
	if err := cfg.Parse(os.Args[1:]); err != nil {
		abort(cfg.FS, err)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	dialer, err := internal.NewDialer(cfg.CaCert)
	if err != nil {
		slog.Error("create HTTP3 dialer", "error", err)
		os.Exit(1)
	}

	client := api.NewClient(dialer, cfg.Registar.URL, []byte(cfg.Secret))
	slog.Debug("client created", "url", cfg.Registar.URL.String())

	switch cfg.Command {
	case "host":
		var hostCfg hostConfig
		if err := hostCfg.Parse(cfg.FS.Args()); err != nil {
			abort(hostCfg.FS, err)
		}
		runHost(ctx, hostCfg, client)
	case "peer":
		var peerCfg peerConfig
		if err := peerCfg.Parse(cfg.FS.Args()); err != nil {
			abort(peerCfg.FS, err)
		}
		runPeer(ctx, peerCfg, client)
	default:
		slog.Error("unknown command", "command", cfg.Command)
		os.Exit(1)
	}

	slog.Info("bye")
}

func runHost(ctx context.Context, cfg hostConfig, client *api.Client) {
	if cfg.GameKey == (auth.Key{}) {
		cfg.GameKey = auth.NewKey()
	}

	hostRegisterer := &host.Registerer{}
	hostForwarder, err := hostRegisterer.Register(ctx, client, cfg.GameKey)
	if err != nil {
		slog.Error("register game", "error", err)
		os.Exit(1)
	}

	slog.Info("game registed", "key", cfg.GameKey.String())

	var eg errgroup.Group
	eg.Go(func() error {
		<-ctx.Done()
		return hostForwarder.Close()
	})
	eg.Go(func() error {
		return hostForwarder.ListenAndForward(ctx, cfg.GameHostPort)
	})

	if err := eg.Wait(); err != nil {
		slog.Error("run host", "error", err)
		os.Exit(1)
	}
}

func runPeer(ctx context.Context, cfg peerConfig, client *api.Client) {
	peerConn := &peer.Connector{}
	peerForwarder, err := peerConn.ConnectAndListen(ctx, client, cfg.GameKey, cfg.PeerID)
	if err != nil {
		slog.Error("connect and listen", "error", err)
		os.Exit(1)
	}

	slog.Info("connected to", "address", peerForwarder.LocalAddr().String())

	var eg errgroup.Group
	eg.Go(func() error {
		<-ctx.Done()
		return peerForwarder.Close()
	})
	eg.Go(func() error {
		return peerForwarder.AcceptAndForward()
	})

	if err := eg.Wait(); err != nil {
		slog.Error("run peer", "error", err)
		os.Exit(1)
	}
}
