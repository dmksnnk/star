package platform

import (
	"context"
	"fmt"
	"net"
)

// MTU is a safe maximum size of a UDP packet.
const MTU = 1200

type LocalDialer struct {
	Dialer net.Dialer
}

func (d *LocalDialer) DialUDP(ctx context.Context, port int) (net.Conn, error) {
	localhost := net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	conn, err := d.Dialer.DialContext(ctx, "udp", localhost.String())
	if err != nil {
		return nil, fmt.Errorf("dial UDP: %w", err)
	}

	return conn, nil
}
