package udp

import (
	"fmt"
	"net"
)

// Pipe creates a pair of connected UDP connections.
func Pipe() (net.Conn, net.Conn, error) {
	localhost := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0, // let the system choose a free port
	}
	llc := &ListenConfig{}
	listener, err := llc.Listen(localhost)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen UDP: %v", err)
	}

	stdConn, err := net.DialUDP("udp", nil, listener.Addr().(*net.UDPAddr))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to dial UDP: %w", err)
	}

	// perform a handshake to establish the connection, because UDP is connectionless
	// and listener does not know about stdConn until data is sent

	handshake := "hello"
	_, err = stdConn.Write([]byte(handshake))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write to UDP connection: %w", err)
	}

	custom, err := listener.Accept()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to accept UDP connection: %w", err)
	}

	buf := make([]byte, len(handshake))
	n, err := custom.Read(buf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read from UDP connection: %w", err)
	}

	res := string(buf[:n])
	if res != handshake {
		return nil, nil, fmt.Errorf("handshake failed, expected: %s, got: %s", handshake, res)
	}

	if err := listener.Close(); err != nil {
		return nil, nil, fmt.Errorf("close listener: %w", err)
	}

	return stdConn, custom, nil
}
