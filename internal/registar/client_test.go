package registar_test

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/dmksnnk/star/internal/cert"
	"github.com/dmksnnk/star/internal/registar"
	"github.com/dmksnnk/star/internal/registar/auth"
	"github.com/dmksnnk/star/internal/registar/control"
	"github.com/dmksnnk/star/internal/registar/integrationtest"
	"github.com/quic-go/quic-go"
)

var secret = []byte("secret")

func TestRegisterHost(t *testing.T) {
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, reg, secret)
	key := auth.NewKey()

	token := auth.NewToken(key, secret)

	tr := &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}
	clientConn, _, err := cc.Host(context.TODO(), tr, srv.URL(), token)
	if err != nil {
		t.Fatalf("register client: %s", err)
	}

	wantCommand := control.ConnectCommand{
		PrivateAddress: netip.MustParseAddrPort("192.168.0.1:1234"),
		PublicAddress:  netip.MustParseAddrPort("192.168.0.2:5678"),
	}

	agent := control.NewAgent()
	agentCalled := make(chan struct{})
	agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		defer close(agentCalled)

		if cmd != wantCommand {
			t.Errorf("expected command %+v, got %+v", wantCommand, cmd)
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, agent, clientConn)

	host, ok := reg.host(key)
	if !ok {
		t.Fatalf("host not registered")
	}

	err = host.ConnectTo(context.TODO(), wantCommand.PublicAddress, wantCommand.PrivateAddress)
	if err != nil {
		t.Fatalf("ConnectTo: %s", err)
	}

	<-agentCalled
}

func TestRegisterJoin(t *testing.T) {
	reg := newFakeRegistar()
	srv := integrationtest.NewServer(t, reg, secret)
	key := auth.NewKey()

	token := auth.NewToken(key, secret)

	tr := &quic.Transport{Conn: integrationtest.NewLocalUDPConn(t)}
	cc := registar.ClientConfig{
		TLSConfig: srv.TLSConfig(),
	}
	clientConn, _, err := cc.Join(context.TODO(), tr, srv.URL(), token)
	if err != nil {
		t.Fatalf("Join: %s", err)
	}

	wantCommand := control.ConnectCommand{
		PrivateAddress: netip.MustParseAddrPort("192.168.0.1:1234"),
		PublicAddress:  netip.MustParseAddrPort("192.168.0.2:5678"),
	}

	agent := control.NewAgent()
	agentCalled := make(chan struct{})
	agent.OnConnectTo(func(ctx context.Context, cmd control.ConnectCommand) (bool, error) {
		defer close(agentCalled)

		if cmd != wantCommand {
			t.Errorf("expected command %+v, got %+v", wantCommand, cmd)
		}

		return true, nil
	})
	integrationtest.ServeAgent(t, agent, clientConn)

	peer, ok := reg.peer(key)
	if !ok {
		t.Fatalf("peer not registered")
	}

	err = peer.ConnectTo(context.TODO(), wantCommand.PublicAddress, wantCommand.PrivateAddress)
	if err != nil {
		t.Fatalf("ConnectTo: %s", err)
	}

	<-agentCalled
}

type fakeRegistar struct {
	mux   sync.Mutex
	hosts map[auth.Key]registar.Peer
	peers map[auth.Key]registar.Peer
}

func newFakeRegistar() *fakeRegistar {
	return &fakeRegistar{
		hosts: make(map[auth.Key]registar.Peer),
		peers: make(map[auth.Key]registar.Peer),
	}
}

func (f *fakeRegistar) NewSessionCert(key auth.Key, csr *x509.CertificateRequest) (*x509.Certificate, *x509.Certificate, error) {
	ca, caPrivKey, err := cert.NewCA()
	if err != nil {
		return nil, nil, fmt.Errorf("create CA: %w", err)
	}

	privkey, err := cert.NewPrivateKey()
	if err != nil {
		return nil, nil, fmt.Errorf("create private key: %w", err)
	}

	peerCert, err := cert.NewIPCert(ca, caPrivKey, privkey.Public(), net.IPv4(127, 0, 0, 1))
	if err != nil {
		return nil, nil, fmt.Errorf("create IP cert: %w", err)
	}

	x509PeerCert, err := x509.ParseCertificate(peerCert)
	if err != nil {
		return nil, nil, fmt.Errorf("parse peer certificate: %w", err)
	}

	return ca, x509PeerCert, nil
}

func (f *fakeRegistar) Host(ctx context.Context, key auth.Key, peer registar.Peer) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.hosts[key] = peer

	return nil
}

func (f *fakeRegistar) Join(ctx context.Context, key auth.Key, peer registar.Peer) error {
	f.mux.Lock()
	defer f.mux.Unlock()

	f.peers[key] = peer
	return nil
}

func (f *fakeRegistar) Close() error {
	return nil
}

func (f *fakeRegistar) host(key auth.Key) (registar.Peer, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.hosts[key]
	return c, ok
}

func (f *fakeRegistar) peer(key auth.Key) (registar.Peer, bool) {
	f.mux.Lock()
	defer f.mux.Unlock()

	c, ok := f.peers[key]
	return c, ok
}
