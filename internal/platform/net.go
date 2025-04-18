package platform

import (
	"context"
	"fmt"
	"net"
)

// MTU is a safe maximum size of a UDP packet.
const MTU = 1200

type LocalListener struct {
	ListenConfig net.ListenConfig
}

func (l LocalListener) ListenUDP(ctx context.Context, port int) (net.PacketConn, error) {
	self := net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	return l.ListenConfig.ListenPacket(ctx, "udp", self.String())
}

type LocalDialer struct {
	Dialer net.Dialer
}

func (d *LocalDialer) DialUDP(ctx context.Context, port int) (net.Conn, error) {
	gameServer := net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	conn, err := d.Dialer.DialContext(ctx, "udp", gameServer.String())
	if err != nil {
		return nil, fmt.Errorf("dial UDP: %w", err)
	}

	return conn, nil
}
