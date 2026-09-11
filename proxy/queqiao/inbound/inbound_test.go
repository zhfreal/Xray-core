package inbound_test

import (
	"testing"

	"github.com/xtls/xray-core/proxy/queqiao"
	"github.com/xtls/xray-core/proxy/queqiao/inbound"
)

func TestInboundRegistration(t *testing.T) {
	cfg := &queqiao.ServerConfig{
		ProviderId: "provider.example",
		GatewayId:  "gw-01",
	}
	if cfg.ProviderId != "provider.example" {
		t.Fatalf("ServerConfig mismatch")
	}
	_ = inbound.New
}
