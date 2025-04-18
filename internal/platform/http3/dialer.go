package http3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

var (
	// ErrDatagramsNotEnabled is returned when client expects datagrams to be enabled on the server,
	// but they are not.
	ErrDatagramsNotEnabled = errors.New("datagrams are not enabled")
	// ErrExtendedConnect is returned when client expects extended CONNECT to be enabled on the server, but it is not.
	ErrExtendedConnect = errors.New("extended CONNECT is not enabled")
)

// HTTP3Dialer dials HTTP/3 connections.
type HTTP3Dialer struct {
	ListenConfig net.ListenConfig
	TLSConfig    *tls.Config
	QUICConfig   *quic.Config
	// EnableExtendedConnect checks if server supports extended CONNECT.
	// If server does not support extended CONNECT, connection will be closed and error will be returned.
	EnableExtendedConnect bool
}

// Dial creates a new HTTP/3 connection to the given address.
func (d *HTTP3Dialer) Dial(ctx context.Context, addr string) (*http3.ClientConn, error) {
	if d.TLSConfig == nil {
		d.TLSConfig = &tls.Config{NextProtos: []string{http3.NextProtoH3}}
	}

	conn, err := quic.DialAddr(ctx, addr, d.TLSConfig, d.QUICConfig)
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}
	// conn will be closed by clientConn
	tr := &http3.Transport{
		EnableDatagrams: d.QUICConfig.EnableDatagrams,
	}
	clientConn := tr.NewClientConn(conn)
	select {
	case <-clientConn.ReceivedSettings():
	case <-clientConn.Context().Done():
		// connection closed
		return nil, clientConn.Context().Err()
	}

	settings := clientConn.Settings()
	if d.QUICConfig.EnableDatagrams && !settings.EnableDatagrams {
		if err := clientConn.CloseWithError(errcode.Exit, ErrDatagramsNotEnabled.Error()); err != nil {
			return nil, fmt.Errorf("close connection: %w", err)
		}
		return nil, ErrDatagramsNotEnabled
	}
	if d.EnableExtendedConnect && !settings.EnableExtendedConnect {
		if err := clientConn.CloseWithError(errcode.Exit, ErrExtendedConnect.Error()); err != nil {
			return nil, fmt.Errorf("close connection: %w", err)
		}
		return nil, ErrExtendedConnect
	}

	return clientConn, nil
}
