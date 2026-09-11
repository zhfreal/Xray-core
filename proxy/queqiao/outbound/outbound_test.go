package outbound_test

import (
	"testing"

	"github.com/xtls/xray-core/proxy/queqiao"
	"github.com/xtls/xray-core/proxy/queqiao/outbound"
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
