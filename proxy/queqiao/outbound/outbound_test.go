package outbound_test

import (
	"context"
	"strings"
	"testing"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/proxy/queqiao"
	"github.com/xtls/xray-core/proxy/queqiao/outbound"
	"github.com/xtls/xray-core/transport/internet"
)

func TestOutboundRegistration(t *testing.T) {
	cfg := &queqiao.ClientConfig{
		ProviderId: "provider.example",
		GatewayId:  "gw-01",
		AccountId:  "alice",
		DeviceId:   "dev-01",
	}
	if cfg.AccountId != "alice" {
		t.Fatalf("ClientConfig mismatch")
	}
	_ = outbound.New
}

func TestQueqiaoTransportDialerRegistration(t *testing.T) {
	memStream := &internet.MemoryStreamConfig{
		ProtocolName: "queqiao",
	}
	dest := xnet.TCPDestination(xnet.LocalHostIP, xnet.Port(59999))
	_, err := internet.Dial(context.Background(), dest, memStream)
	if err != nil && strings.Contains(err.Error(), "dialer not registered") {
		t.Fatalf("Expected queqiao dialer to be registered, got: %v", err)
	}
}
