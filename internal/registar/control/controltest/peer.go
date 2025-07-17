package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/gob"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/control/controltest/config"
	"github.com/quic-go/quic-go"
)

func main() {
	var cfg config.Config
	if err := gob.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		slog.Error("decode config", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenAddress)
	if err != nil {
		slog.Error("resolve udp listen address", "err", err)
		os.Exit(1)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		slog.Error("listen UDP", "err", err)
		os.Exit(1)
	}

	transport := &quic.Transport{
		Conn: conn,
	}
	defer transport.Close()

	tlsConfig, err := cfg.Cert.TLSConfig()
	if err != nil {
		slog.Error("create TLS config", "err", err)
		os.Exit(1)
	}

	connector := control.NewConnector(transport, tlsConfig, control.WithLogger(logger))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := connector.Connect(ctx, cfg.PeerPrivateAddress, cfg.PeerPublicAddress)
	if err != nil {
		slog.Error("connector.Connect failed", "err", err)
		os.Exit(1)
	}
	slog.Info("connected")

	if cfg.Mode == "client" {
		runClient(stream)
	} else {
		runServer(stream)
	}
}

func runClient(stream http3platform.Stream) {
	want := make([]byte, 1<<20)
	rand.Read(want)

	_, err := io.CopyBuffer(stream, bytes.NewBuffer(want), make([]byte, 1024))
	if err != nil {
		slog.Error("write to stream", "err", err)
		os.Exit(1)
	}
	slog.Info("client: sent data")
	// close send direction
	if err := stream.Close(); err != nil {
		slog.Error("close stream", "err", err)
		os.Exit(1)
	}

	got := make([]byte, len(want))
	if _, err := io.ReadFull(stream, got); err != nil {
		slog.Error("client: read stream", "err", err)
		os.Exit(1)
	}
	slog.Info("client: received data")

	if !bytes.Equal(want, got) {
		slog.Error("data differs")
		os.Exit(1)
	}
}

func runServer(stream http3platform.Stream) {
	got, err := io.ReadAll(stream)
	if err != nil {
		slog.Error("read stream", "err", err)
		os.Exit(1)
	}
	slog.Info("server: received data")

	_, err = io.CopyBuffer(stream, bytes.NewBuffer(got), make([]byte, 1024))
	if err != nil {
		slog.Error("write to stream", "err", err)
		os.Exit(1)
	}
	if err := stream.Close(); err != nil {
		slog.Error("close stream", "err", err)
		os.Exit(1)
	}
	slog.Info("server: sent data")
}
