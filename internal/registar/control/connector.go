package control

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/dmksnnk/star/internal/errcode"
	"github.com/dmksnnk/star/internal/platform/http3platform"
	"github.com/dmksnnk/star/internal/platform/httpplatform"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/sync/errgroup"
)

var errConnectionRejected = errors.New("connection rejected")

// Connector does NAT hole punching via HTTP/3 trying to directly connect two peers.
type Connector struct {
	id        string
	transport *quic.Transport
	tlsConf   *tls.Config
	quicConf  *quic.Config
	logger    *slog.Logger
}

func NewConnector(transport *quic.Transport, tlsConf *tls.Config, quicConf *quic.Config, logger *slog.Logger) *Connector {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(fmt.Sprintf("read from random: %v", err))
	}
	return &Connector{
		id:        hex.EncodeToString(id),
		transport: transport,
		tlsConf:   tlsConf,
		quicConf:  quicConf,
		logger:    logger,
	}
}

func (c *Connector) Connect(ctx context.Context, public, private string) (http3platform.Stream, error) {
	earlyListener, err := c.transport.ListenEarly(c.tlsConf, c.quicConf)
	if err != nil {
		return nil, fmt.Errorf("listen early: %w", err)
	}

	accepted := make(chan *http3.Stream)
	serveCtx, serveCancel := context.WithCancel(ctx)
	defer serveCancel()
	srv := &http3.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peerID := r.Header.Get("X-Connector-ID")
			if peerID == "" {
				http.Error(w, "missing X-Connector-ID", http.StatusBadRequest)
				return
			}

			if peerID > c.id {
				c.logger.Debug("tie-breaking: peer has larger ID, rejecting connection", "peer_id", peerID)
				http.Error(w, "rejected by tie-breaker", http.StatusConflict)
				return
			}
			c.logger.Debug("tie-breaking: we have larger or equal ID, accepting server-side connection", "peer_id", peerID)

			conn := w.(http3.Hijacker).Connection()
			// wait for the client's SETTINGS
			select {
			case <-conn.ReceivedSettings():
			case <-time.After(10 * time.Second):
				// didn't receive SETTINGS within 10 seconds
				http.Error(w, "timeout waiting for client's SETTINGS", http.StatusRequestTimeout)
				return
			}

			settings := conn.Settings()
			if !settings.EnableDatagrams {
				http.Error(w, "datagrams are not enabled", http.StatusBadRequest)
				return
			}

			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()

			streamer := w.(http3.HTTPStreamer)
			select {
			case accepted <- streamer.HTTPStream():
				c.logger.Debug("accepted connection", "remote_addr", conn.RemoteAddr().String())
				return
			case <-serveCtx.Done():
				err := fmt.Errorf("server connection cancelled: %w", context.Cause(serveCtx))
				conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeRequestRejected), err.Error())
				return
			}
		}),
		TLSConfig:       http3.ConfigureTLSConfig(c.tlsConf),
		EnableDatagrams: true,
	}

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		defer c.logger.Debug("server exited")
		for {
			qConn, err := earlyListener.Accept(serveCtx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				if errors.Is(err, quic.ErrServerClosed) {
					return nil
				}

				return fmt.Errorf("accept: %w", err)
			}

			served := make(chan error)
			go func() {
				c.logger.Debug("serving connection", "remote_addr", qConn.RemoteAddr().String())
				served <- srv.ServeQUICConn(qConn)
			}()

			select {
			case err := <-served:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}

				var appErr *quic.ApplicationError
				if errors.As(err, &appErr) {
					// client cancelled request
					if appErr.ErrorCode == quic.ApplicationErrorCode(http3.ErrCodeRequestCanceled) {
						c.logger.Debug("client cancelled request")
						continue
					}
				}
				return fmt.Errorf("serve QUIC conn: %w", err)
			case <-serveCtx.Done():
				c.logger.Debug("closing connection")
				err := fmt.Errorf("server serve cancelled: %w", context.Cause(serveCtx))
				qConn.CloseWithError(quic.ApplicationErrorCode(errcode.Cancelled), err.Error())
				// wait for goroutine to exit
				err = <-served
				var appErr *quic.ApplicationError
				if errors.As(err, &appErr) {
					// close connection from above
					if appErr.ErrorCode == quic.ApplicationErrorCode(errcode.Cancelled) {
						return nil
					}
				}
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}

			// var toErr *quic.IdleTimeoutError
			// if errors.As(err, &toErr) && toErr.Timeout() {
			// 	continue
			// }

			// var appErr *quic.ApplicationError
			// if errors.As(err, &appErr) {
			// 	// client cancelled request
			// 	if appErr.ErrorCode == quic.ApplicationErrorCode(http3.ErrCodeRequestCanceled) {
			// 		c.logger.Debug("client cancelled request")
			// 		continue
			// 	}
			// 	// stop server
			// 	if appErr.ErrorCode == quic.ApplicationErrorCode(http3.ErrCodeRequestRejected) {
			// 		c.logger.Debug("server stopped")
			// 		return nil
			// 	}
			// }

		}
	})

	publicDialled := make(chan *http3.RequestStream)
	publicReqCtx, publicReqCancel := context.WithCancel(ctx)
	defer publicReqCancel()
	eg.Go(func() error {
		defer c.logger.Debug("public dialer exited")
		for {
			conn, err := c.dial(publicReqCtx, public)
			if err != nil {
				if isHandshakeTimeoutError(err) {
					continue
				}
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("dial public address: %w", err)
			}

			stream, err := c.sendRequest(publicReqCtx, conn, public)
			if err != nil {
				if isHandshakeTimeoutError(err) {
					continue
				}

				if errors.Is(err, errConnectionRejected) {
					c.logger.Debug("public dial rejected by peer, retrying")
					conn.CloseWithError(http3.ErrCodeRequestCanceled, "closing after rejection")
					continue
				}
				if errors.Is(err, context.Canceled) {
					err := fmt.Errorf("public connection cancelled: %w", context.Cause(publicReqCtx))
					return conn.CloseWithError(http3.ErrCodeRequestCanceled, err.Error())
				}

				return fmt.Errorf("send request to public address: %w", err)
			}

			select {
			case publicDialled <- stream:
				return nil
			case <-publicReqCtx.Done():
				err := fmt.Errorf("public connection cancelled: %w", context.Cause(publicReqCtx))
				return conn.CloseWithError(http3.ErrCodeRequestCanceled, err.Error())
			}
		}
	})

	privateDialled := make(chan *http3.RequestStream)
	privateReqCtx, privateReqCancel := context.WithCancel(ctx)
	defer privateReqCancel()
	eg.Go(func() error {
		defer c.logger.Debug("private dialer exited")
		for {
			conn, err := c.dial(privateReqCtx, private)
			if err != nil {
				if isHandshakeTimeoutError(err) {
					continue
				}
				if errors.Is(err, context.Canceled) {
					return nil
				}

				return fmt.Errorf("dial private address: %w", err)
			}

			stream, err := c.sendRequest(privateReqCtx, conn, private)
			if err != nil {
				if isHandshakeTimeoutError(err) {
					continue
				}
				if errors.Is(err, errConnectionRejected) {
					c.logger.Debug("private dial rejected by peer, retrying")
					conn.CloseWithError(http3.ErrCodeRequestCanceled, "closing after rejection")
					continue
				}
				if errors.Is(err, context.Canceled) {
					err := fmt.Errorf("private connection cancelled: %w", context.Cause(privateReqCtx))
					return conn.CloseWithError(http3.ErrCodeRequestCanceled, err.Error())
				}

				return fmt.Errorf("send request to private address: %w", err)
			}

			select {
			case privateDialled <- stream:
				return nil
			case <-privateReqCtx.Done():
				err := fmt.Errorf("private connection cancelled: %w", context.Cause(privateReqCtx))
				return conn.CloseWithError(http3.ErrCodeRequestCanceled, err.Error())
			}
		}
	})

	select {
	case stream := <-accepted:
		earlyListener.Close()
		// serveCancel()
		publicReqCancel()
		privateReqCancel()
		c.logger.Info("connection established via server")
		return stream, eg.Wait()
	case stream := <-publicDialled:
		earlyListener.Close()
		serveCancel()
		srv.Close()
		privateReqCancel()
		c.logger.Info("connection established via public dial")
		return stream, eg.Wait()
	case stream := <-privateDialled:
		earlyListener.Close()
		serveCancel()
		srv.Close()
		publicReqCancel()
		c.logger.Info("connection established via private dial")
		return stream, eg.Wait()
	case <-ctx.Done():
		earlyListener.Close()
		serveCancel()
		srv.Close()
		publicReqCancel()
		privateReqCancel()
		return nil, eg.Wait()
	}
}

func (c *Connector) dial(ctx context.Context, addr string) (*http3.ClientConn, error) {
	dialer := &http3platform.HTTP3Dialer{
		Transport:  c.transport,
		TLSConfig:  c.tlsConf.Clone(),
		QUICConfig: c.quicConf.Clone(),
	}

	conn, err := dialer.Dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial HTTP3: %w", err)
	}

	return conn, nil
}

func (c *Connector) sendRequest(ctx context.Context, conn *http3.ClientConn, addr string) (*http3.RequestStream, error) {
	req, err := c.newRequest(ctx, addr)
	if err != nil {
		return nil, err
	}

	// DO NOT close resp.Body here, it will close the stream

	reqStream, err := conn.OpenRequestStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open request stream: %w", err)
	}

	if err := reqStream.SendRequestHeader(req); err != nil {
		return nil, fmt.Errorf("send request header: %w", err)
	}

	httpResp, err := reqStream.ReadResponse()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		httpResp.Body.Close()
		if httpResp.StatusCode == http.StatusConflict {
			return nil, errConnectionRejected
		}
		return nil, httpplatform.NewBadStatusCodeError(httpResp.StatusCode, httpResp.Body)
	}

	return reqStream, nil
}

func (c *Connector) newRequest(ctx context.Context, addr string) (*http.Request, error) {
	u := &url.URL{
		Scheme: "https",
		Host:   addr,
		Path:   "/connect",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Connector-ID", c.id)
	return req, nil
}

func isHandshakeTimeoutError(err error) bool {
	var toErr *quic.HandshakeTimeoutError
	return errors.As(err, &toErr) && toErr.Timeout()
}

func isRequestRejectedError(err error) bool {
	var appErr *http3.Error
	return errors.As(err, &appErr) && appErr.ErrorCode == http3.ErrCodeRequestRejected
}
