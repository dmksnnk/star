package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/dmksnnk/star/internal/registar/p2p"
	"github.com/dmksnnk/star/internal/registar/p2p/p2ptest/config"
	"github.com/quic-go/quic-go"
)

func main() {
	var cfg config.Config
	if err := gob.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		abort("decode config", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger = logger.With("peer", cfg.Name)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(cfg.ListenAddress))
	if err != nil {
		abort("listen UDP", err)
	}

	logger.Info("listening UDP", slog.String("addr", conn.LocalAddr().String()))

	transport := &quic.Transport{
		Conn: conn,
	}
	defer transport.Close()

	tlsConfig, err := cfg.Cert.TLSConfig()
	if err != nil {
		abort("create TLS config", err)
	}

	connector := p2p.NewConnector(transport, tlsConfig, p2p.WithLogger(logger))

	ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	quicConn, err := connector.Connect(ctx, cfg.PeerPublicAddress, cfg.PeerPrivateAddress)
	if err != nil {
		abort("connector.Connect failed", err)
	}
	slog.Info("connected")

	if cfg.Mode == "client" {
		if err := runClient(ctx, quicConn); err != nil {
			abort("run client", err)
		}
	} else {
		if err := runServer(ctx, quicConn); err != nil {
			abort("run server", err)
		}
	}

	slog.Info("peer done")
}

func runClient(ctx context.Context, conn *quic.Conn) error {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	want := make([]byte, 1<<20)
	rand.Read(want)

	_, err = io.CopyBuffer(stream, bytes.NewBuffer(want), make([]byte, 1024))
	if err != nil {
		return fmt.Errorf("write to stream: %w", err)
	}

	slog.Info("client: sent data")

	// close send direction
	if err := stream.Close(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}

	got := make([]byte, len(want))
	if _, err := io.ReadFull(stream, got); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	slog.Info("client: received data")

	if !bytes.Equal(want, got) {
		return fmt.Errorf("data differs")
	}

	return conn.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "done")
}

func runServer(ctx context.Context, conn *quic.Conn) error {
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept stream: %w", err)
	}

	got, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("read stream: %w", err)
	}

	slog.Info("server: received data")

	_, err = io.CopyBuffer(stream, bytes.NewBuffer(got), make([]byte, 1024))
	if err != nil {
		return fmt.Errorf("write to stream: %w", err)
	}

	slog.Info("server: sent data")

	if err := stream.Close(); err != nil {
		return fmt.Errorf("close stream: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-conn.Context().Done(): // wait for connection to close from client side
		return nil
	}
}

func abort(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
