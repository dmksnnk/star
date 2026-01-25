package discovery

import (
	"context"
	"crypto/rand"
	"encoding"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"time"
)

// A tiny STUN-like protocol over UDP.
//
// Wire format (big-endian):
//   [0:4]  Magic: 'S','T','A','R'
//   [4]    Version: 0x01
//   [5]    Type: 0x01=request, 0x02=response
//   [6:22] TxID: 16 random bytes (echoed in response)
// Request has no body.
// Response body:
//   [22:] Address in netip.AddrPort binary format

const (
	magic     uint32 = 0x53544152 // 'STAR'
	version   byte   = 0x01
	tRequest  byte   = 0x01
	tResponse byte   = 0x02
)

var errInvalidResponse = errors.New("discovery: invalid response")

// Request represents a binding request message.
type Request struct {
	TxID [16]byte
}

// NewRequest creates a new Request with a random transaction ID.
func NewRequest() (Request, error) {
	var r Request
	if _, err := rand.Read(r.TxID[:]); err != nil {
		return Request{}, err
	}
	return r, nil
}

var (
	_ encoding.BinaryMarshaler   = Request{}
	_ encoding.BinaryUnmarshaler = &Request{}
	_ encoding.BinaryMarshaler   = Response{}
	_ encoding.BinaryUnmarshaler = &Response{}
)

// MarshalBinary encodes the request into its wire format.
func (r Request) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 22)
	binary.BigEndian.PutUint32(buf[0:4], magic)
	buf[4] = version
	buf[5] = tRequest
	copy(buf[6:22], r.TxID[:])
	return buf, nil
}

// UnmarshalBinary decodes a request from its wire format.
func (r *Request) UnmarshalBinary(p []byte) error {
	if len(p) < 22 {
		return errInvalidResponse
	}
	if binary.BigEndian.Uint32(p[0:4]) != magic {
		return errInvalidResponse
	}
	if p[4] != version || p[5] != tRequest {
		return errInvalidResponse
	}
	copy(r.TxID[:], p[6:22])
	return nil
}

// Response represents a binding response message.
type Response struct {
	TxID [16]byte
	Addr netip.AddrPort
}

// MarshalBinary encodes the response into its wire format.
func (r Response) MarshalBinary() ([]byte, error) {
	var resp []byte

	resp = binary.BigEndian.AppendUint32(resp, magic)
	resp = append(resp, version, tResponse)
	resp = append(resp, r.TxID[:]...)
	return r.Addr.AppendBinary(resp)
}

// UnmarshalBinary decodes the response from its wire format.
func (r *Response) UnmarshalBinary(p []byte) error {
	if len(p) < 22 { // minimum response size
		return errInvalidResponse
	}
	if binary.BigEndian.Uint32(p[0:4]) != magic || p[4] != version || p[5] != tResponse {
		return errInvalidResponse
	}
	copy(r.TxID[:], p[6:22])

	var addr netip.AddrPort
	if err := addr.UnmarshalBinary(p[22:]); err != nil {
		return err
	}

	r.Addr = addr
	return nil
}

// Bind sends a binding request to the given server and returns the public
// address as observed by the server.
func Bind(ctx context.Context, conn net.PacketConn, server netip.AddrPort) (netip.AddrPort, error) {
	req, err := NewRequest()
	if err != nil {
		return netip.AddrPort{}, err
	}
	pkt, err := req.MarshalBinary()
	if err != nil {
		return netip.AddrPort{}, err
	}

	buf := make([]byte, 1024)
	for {
		if err := ctx.Err(); err != nil {
			return netip.AddrPort{}, err
		}

		if _, err := conn.WriteTo(pkt, net.UDPAddrFromAddrPort(server)); err != nil {
			return netip.AddrPort{}, err
		}

		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))

		for {
			n, raddr, err := conn.ReadFrom(buf)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					break // resend request
				}

				conn.SetDeadline(time.Time{}) // clear deadline

				return netip.AddrPort{}, err
			}

			rUDPAddr, ok := raddr.(*net.UDPAddr)
			if !ok {
				continue
			}

			// only accept from the server address we sent to
			if server.Compare(rUDPAddr.AddrPort()) != 0 {
				continue
			}

			var resp Response
			if err := resp.UnmarshalBinary(buf[:n]); err != nil {
				continue // keep reading until timeout
			}

			if resp.TxID != req.TxID {
				continue
			}

			conn.SetDeadline(time.Time{}) // clear deadline
			return resp.Addr, nil
		}
	}
}

// Server represents a discovery server.
type Server struct {
	done chan struct{}
}

// NewServer creates a new discovery server.
func NewServer() *Server {
	return &Server{
		done: make(chan struct{}),
	}
}

// Serve handles incoming binding requests on the provided PacketConn.
// It blocks until the server is closed.
func (s *Server) Serve(conn net.PacketConn) error {
	buf := make([]byte, 1024)
	for {
		select {
		case <-s.done:
			return nil
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, raddr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			conn.SetDeadline(time.Time{}) // clear deadline

			return err
		}

		var req Request
		if err := req.UnmarshalBinary(buf[:n]); err != nil {
			continue // invalid, ignore
		}

		rUDPAddr, ok := raddr.(*net.UDPAddr)
		if !ok {
			continue // not UDP, ignore
		}

		resp := Response{TxID: req.TxID, Addr: rUDPAddr.AddrPort()}
		data, err := resp.MarshalBinary()
		if err != nil {
			conn.SetDeadline(time.Time{})
			return err
		}

		_, err = conn.WriteTo(data, raddr)
		if err != nil {
			conn.SetDeadline(time.Time{})
			return err
		}
	}
}

// Listen starts listening on the given address for incoming discovery requests.
func (s *Server) Listen(addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	return s.Serve(conn)
}

// Close stops the discovery server.
// Wait for any ongoing Serve calls to return to release resources.
func (s *Server) Close() error {
	close(s.done)
	return nil
}
