//go:build integration

package p2p_test

import (
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/dmksnnk/star/internal/registar/p2p/p2ptest/config"
	"golang.org/x/sync/errgroup"

	gont "cunicu.li/gont/v2/pkg"
	gontops "cunicu.li/gont/v2/pkg/options"
	cmdops "cunicu.li/gont/v2/pkg/options/cmd"
)

// TestConnectorSingleSwitch tests connection over single switch.
//
//	peer1 <-> sw1 <-> peer2
func TestConnectorSingleSwitch(t *testing.T) {
	peerPath := buildPeer(t)

	n, err := gont.NewNetwork(t.Name())
	if err != nil {
		t.Fatalf("create network: %s", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close network: %s", err)
		}
	})

	sw1, err := n.AddSwitch("sw1")
	if err != nil {
		t.Fatalf("create switch sw1: %v", err)
	}

	peer1, err := n.AddHost("peer1",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.2/24")))
	if err != nil {
		t.Fatalf("create peer1: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.3/24")))
	if err != nil {
		t.Fatalf("create peer2: %v", err)
	}

	_, err = peer1.Ping(peer2)
	if err != nil {
		t.Fatalf("Failed to ping peer1 -> peer2: %v", err)
	}

	cfg1 := config.Config{
		Name:               "peer1",
		ListenAddress:      netip.MustParseAddrPort("10.0.1.2:8000"),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.3:8000"), // peer2 address
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.3:8000"), // peer2 address
		Mode:               "client",
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.2"), net.ParseIP("10.0.0.1")),
	}
	peer1Cmd := peer1.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg1)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	cfg2 := config.Config{
		Name:               "peer2",
		ListenAddress:      netip.MustParseAddrPort("10.0.1.3:8000"),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.2:8000"), // peer1 address
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.2:8000"), // peer1 address
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.3"), net.ParseIP("10.0.1.1")),
		Mode:               "server",
	}
	peer2Cmd := peer2.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg2)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	var eg errgroup.Group
	eg.Go(func() error {
		return peer1Cmd.Run()
	})
	eg.Go(func() error {
		return peer2Cmd.Run()
	})

	if err := eg.Wait(); err != nil {
		t.Errorf("failed: %v", err)
	}
}

// TestConnectorNAT
//
//	peer1 <-> sw1 <-> nat1 <-> sw2 <-> peer2
func TestConnectorNAT(t *testing.T) {
	peerPath := buildPeer(t)

	n, err := gont.NewNetwork(t.Name())
	if err != nil {
		t.Fatalf("create network: %s", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close network: %s", err)
		}
	})

	sw1, err := n.AddSwitch("sw1")
	if err != nil {
		t.Fatalf("create switch sw1: %v", err)
	}

	sw2, err := n.AddSwitch("sw2")
	if err != nil {
		t.Fatalf("create switch sw2: %v", err)
	}

	peer1, err := n.AddHost("peer1",
		gontops.DefaultGatewayIP("10.0.1.1"),
		gont.NewInterface("veth0", sw1,
			gontops.AddressIP("10.0.1.2/24")))
	if err != nil {
		t.Fatalf("create peer1 host: %v", err)
	}

	peer2, err := n.AddHost("peer2",
		gontops.DefaultGatewayIP("10.0.2.1"),
		gont.NewInterface("veth0", sw2,
			gontops.AddressIP("10.0.2.2/24")))
	if err != nil {
		t.Fatalf("create peer2 host: %v", err)
	}

	_, err = n.AddNAT("nat1",
		gont.NewInterface("veth0", sw1, gontops.SouthBound,
			gontops.AddressIP("10.0.1.1/24")),
		gont.NewInterface("veth1", sw2, gontops.NorthBound,
			gontops.AddressIP("10.0.2.1/24")))
	if err != nil {
		t.Fatalf("create NAT: %s", err)
	}

	_, err = peer1.Ping(peer2)
	if err != nil {
		t.Fatalf("Failed to ping peer1 -> peer2: %v", err)
	}

	cfg1 := config.Config{
		Name:               "peer1",
		ListenAddress:      netip.AddrPortFrom(netip.IPv4Unspecified(), 8000),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.1.1:8000"), // public address of peer2, as seen through NAT
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.2.2:8000"),
		Mode:               "client",
		Cert:               newCertConfig(t, net.ParseIP("10.0.1.2"), net.ParseIP("10.0.2.1")),
	}
	peer1Cmd := peer1.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg1)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	cfg2 := config.Config{
		Name:               "peer2",
		ListenAddress:      netip.AddrPortFrom(netip.IPv4Unspecified(), 8000),
		PeerPublicAddress:  netip.MustParseAddrPort("10.0.2.1:8000"), // public address of peer1, as seen trough NAT
		PeerPrivateAddress: netip.MustParseAddrPort("10.0.1.2:8000"),
		Mode:               "server",
		Cert:               newCertConfig(t, net.ParseIP("10.0.2.2"), net.ParseIP("10.0.1.1")),
	}
	peer2Cmd := peer2.Command(peerPath,
		cmdops.Stdin(stdinConfig(t, cfg2)),
		cmdops.Stderr(os.Stderr),
		cmdops.Stdout(os.Stdout),
	)

	var eg errgroup.Group
	eg.Go(func() error {
		return peer1Cmd.Run()
	})
	eg.Go(func() error {
		return peer2Cmd.Run()
	})

	if err := eg.Wait(); err != nil {
		t.Errorf("failed: %v", err)
	}
}
