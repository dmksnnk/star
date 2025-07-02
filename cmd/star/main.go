package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmksnnk/star/internal"
	"github.com/dmksnnk/star/internal/forwarder"
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
	slog.Debug("client created", "url", cfg.Registar.String())

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

	listener, err := forwarder.RegisterGame(ctx, client, cfg.GameKey)
	if err != nil {
		slog.Error("register game", "error", err)
		os.Exit(1)
	}

	slog.Info("game registed", "key", cfg.GameKey.String())

	host := forwarder.NewHost(
		forwarder.WithNotifyDisconnect(func(err error) {
			if err != nil {
				slog.Error("host disconnected with error", "error", err)
			}
		}),
	)

	var eg errgroup.Group
	eg.Go(func() error {
		return host.Forward(ctx, listener, cfg.GameHostPort)
	})
	slog.Info("host forwarding started", "game_port", cfg.GameHostPort)

	<-ctx.Done()
	if err := host.Close(); err != nil {
		slog.Error("close host", "error", err)
		os.Exit(1)
	}

	if err := eg.Wait(); err != nil {
		slog.Error("run host", "error", err)
		os.Exit(1)
	}
}

func runPeer(ctx context.Context, cfg peerConfig, client *api.Client) {
	peer, err := forwarder.PeerListenLocalUDP(ctx, client)
	if err != nil {
		slog.Error("peer listen local UDP", "error", err)
		os.Exit(1)
	}
	defer peer.Close()

	slog.Info("peer listening on", "address", peer.Addr().String())

	if err := peer.ConnectAndForward(ctx, cfg.GameKey, cfg.PeerID); err != nil {
		slog.Error("connect and listen", "error", err)
		os.Exit(1)
	}
}
