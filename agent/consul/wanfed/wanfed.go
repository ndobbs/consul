package wanfed

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/consul/agent/pool"
	"github.com/hashicorp/consul/tlsutil"
	"github.com/hashicorp/memberlist"
)

type MeshGatewayResolver func(datacenter string) string

func NewTransport(
	logger *log.Logger,
	tlsConfigurator *tlsutil.Configurator,
	transport memberlist.NodeAwareTransport,
	datacenter string,
	gwResolver MeshGatewayResolver, // TODO: move this to the router? FindGatewayRoute?
) (*Transport, error) {
	// TODO(rb)
	if logger == nil {
		return nil, errors.New("wanfed: logger is nil")
	}
	// TODO(rb)
	if tlsConfigurator == nil {
		return nil, errors.New("wanfed: tlsConfigurator is nil")
	}
	// TODO(rb)
	if gwResolver == nil {
		return nil, errors.New("wanfed: gwResolver is nil")
	}

	t := &Transport{
		NodeAwareTransport: transport,
		logger:             logger,
		tlsConfigurator:    tlsConfigurator,
		datacenter:         datacenter,
		gwResolver:         gwResolver,
	}
	return t, nil
}

type Transport struct {
	memberlist.NodeAwareTransport

	logger          *log.Logger
	tlsConfigurator *tlsutil.Configurator
	datacenter      string
	gwResolver      MeshGatewayResolver
}

var _ memberlist.NodeAwareTransport = (*Transport)(nil)

func (t *Transport) WriteToAddress(b []byte, addr memberlist.Address) (time.Time, error) {
	node, dc, err := splitNodeName(addr.Name)
	if err != nil {
		return time.Time{}, err
	}

	if dc != t.datacenter {
		t.logger.Printf("[DEBUG] wanfed.packet: dest.dc=%q in src.dc=%q", dc, t.datacenter)

		gwAddr := t.gwResolver(dc)
		if gwAddr == "" {
			return time.Time{}, fmt.Errorf("could not find suitable mesh gateway to dial dc=%q", dc)
			// TODO: return structs.ErrDCNotAvailable
		}

		conn, err := t.dial(dc, node, pool.ALPN_WANGossipPacket, gwAddr)
		if err != nil {
			return time.Time{}, err
		}
		defer conn.Close()

		if _, err = conn.Write(b); err != nil {
			return time.Time{}, err
		}

		return time.Now(), nil
	}

	return t.NodeAwareTransport.WriteToAddress(b, addr)
}

func (t *Transport) DialAddressTimeout(addr memberlist.Address, timeout time.Duration) (net.Conn, error) {
	node, dc, err := splitNodeName(addr.Name)
	if err != nil {
		return nil, err
	}

	if dc != t.datacenter {
		t.logger.Printf("[DEBUG] wanfed.stream: dest.dc=%q in src.dc=%q", dc, t.datacenter)

		gwAddr := t.gwResolver(dc)
		if gwAddr == "" {
			return nil, fmt.Errorf("could not find suitable mesh gateway to dial dc=%q", dc)
			// TODO: return structs.ErrDCNotAvailable
		}

		return t.dial(dc, node, pool.ALPN_WANGossipStream, gwAddr)
	}

	return t.NodeAwareTransport.DialAddressTimeout(addr, timeout)
}

// NOTE: There is a close mirror of this method in agent/pool/pool.go:DialTimeoutWithRPCType
func (t *Transport) dial(dc, nodeName, nextProto, addr string) (net.Conn, error) {
	t.logger.Printf("[DEBUG] wanfed: dialing dc=%q node=%q proto=%q via mgw-addr=%q",
		dc, nodeName, nextProto, addr)

	wrapper := t.tlsConfigurator.OutgoingALPNRPCWrapper()
	if wrapper == nil {
		return nil, fmt.Errorf("wanfed: cannot dial via a mesh gateway when outgoing TLS is disabled")
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	rawConn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	if tcp, ok := rawConn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetNoDelay(true)
	}

	tlsConn, err := wrapper(dc, nodeName, nextProto, rawConn)
	if err != nil {
		return nil, err
	}

	return tlsConn, nil
}

func splitNodeName(fullName string) (nodeName, dc string, err error) {
	parts := strings.Split(fullName, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("node name does not encode a datacenter: %s", fullName)
	}
	return parts[0], parts[1], nil
}
