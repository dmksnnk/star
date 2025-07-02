package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/quic-go/quic-go/http3"
)

const (
	// CapsuleProtocolHeaderValueTrue is a boolenan "true"
	// from Structured Field Values for HTTP (https://www.rfc-editor.org/rfc/rfc8941#name-booleans)
	CapsuleProtocolHeaderValueTrue = "?1"
)

// Client is a client to registar service.
type Client struct {
	dialer *http3platform.HTTP3Dialer

	base   *url.URL
	secret []byte

	mux        sync.Mutex
	clientConn *http3.ClientConn
}

// NewClient creates a new client with the given base URL and secret.
// Secret is used to sign game keys.
func NewClient(dialer *http3platform.HTTP3Dialer, base *url.URL, secret []byte) *Client {
	c := Client{
		dialer: dialer,
		base:   base,
		secret: secret,
	}

	return &c
}

// RegisterGame registers a game under a key with the given server address.
func (c *Client) RegisterGame(ctx context.Context, key auth.Key) (*http3.RequestStream, error) {
	token := auth.NewToken(key, c.secret)
	path := "/games/" + token.String()
	reqStream, err := c.connect(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("CONNECT: %w", err)
	}

	return reqStream, nil
}

// ConnectGame connects to a game with the given key.
func (c *Client) ConnectGame(ctx context.Context, key auth.Key, peerID string) (*http3.RequestStream, error) {
	token := auth.NewToken(key, c.secret)
	path := "/games/" + token.String() + "/connect/" + peerID
	reqStream, err := c.connect(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("CONNECT: %w", err)
	}

	return reqStream, nil
}

// Forward asks server to forward a connection from a peer.
// Returns a datagram connection to the server, to which server will forward the peer's data.
func (c *Client) Forward(ctx context.Context, key auth.Key, peerID string) (*http3.RequestStream, error) {
	token := auth.NewToken(key, c.secret)
	path := "/games/" + token.String() + "/forward/" + peerID
	reqStream, err := c.connect(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("CONNECT: %w", err)
	}

	return reqStream, nil
}

func (c *Client) Close() error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.clientConn == nil {
		return nil
	}

	if err := c.clientConn.CloseWithError(errcode.Exit, "client closed"); err != nil {
		return fmt.Errorf("close client connection: %w", err)
	}

	return nil
}

func (c *Client) conn(ctx context.Context) (*http3.ClientConn, error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.clientConn != nil {
		return c.clientConn, nil
	}

	clientConn, err := c.dialer.Dial(ctx, ensurePort(c.base.Host))
	if err != nil {
		return nil, fmt.Errorf("dial HTTP3: %w", err)
	}

	c.clientConn = clientConn

	return c.clientConn, nil
}

func ensurePort(host string) string {
	_, _, err := net.SplitHostPort(host)
	if err != nil {
		return net.JoinHostPort(host, "443")
	}

	return host
}

func (c *Client) connect(ctx context.Context, path string) (*http3.RequestStream, error) {
	clientConn, err := c.conn(ctx)
	if err != nil {
		return nil, err
	}

	reqStream, err := clientConn.OpenRequestStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open request stream: %w", err)
	}

	u := c.resolvePath(path)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodConnect, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Proto = "connect-udp"
	httpReq.Header.Set(http3.CapsuleProtocolHeader, CapsuleProtocolHeaderValueTrue)

	if err := reqStream.SendRequestHeader(httpReq); err != nil {
		return nil, fmt.Errorf("send request header: %w", err)
	}

	httpResp, err := reqStream.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	// DO NOT close resp.Body here, it will close the stream

	if httpResp.StatusCode != http.StatusOK {
		httpResp.Body.Close()
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	return reqStream, nil
}

func (c *Client) resolvePath(path string) string {
	return c.base.ResolveReference(&url.URL{Path: path}).String()
}
