package conf_test

import (
	"encoding/json"
	"testing"
	"github.com/xtls/xray-core/infra/conf"
)

func TestQueqiaoConfig_StreamSettingsSplit(t *testing.T) {
	inboundRaw := `{
		"providerId": "provider.example",
		"gatewayId": "gw-01",
		"allowPrivate": false,
		"accounts": [
			{
				"accountId": "alice",
				"devices": [{"deviceId": "dev-01", "publicKey": "base64ed25519pubkey"}]
			}
		]
	}`
	var srvCfg conf.QueqiaoServerConfig
	if err := json.Unmarshal([]byte(inboundRaw), &srvCfg); err != nil {
		t.Fatalf("Failed to unmarshal QueqiaoServerConfig: %v", err)
	}
	if srvCfg.ProviderID != "provider.example" || srvCfg.GatewayID != "gw-01" {
		t.Fatalf("ProviderID or GatewayID mismatch")
	}
	if len(srvCfg.Accounts) != 1 || srvCfg.Accounts[0].AccountID != "alice" {
		t.Fatalf("Accounts improperly parsed")
	}

	streamRaw := `{
		"hopPortCount": 8,
		"congestion": "bbr",
		"quicPool": true,
		"chunkSize": 32768
	}`
	var qCfg conf.QueqiaoConfig
	if err := json.Unmarshal([]byte(streamRaw), &qCfg); err != nil {
		t.Fatalf("Failed to unmarshal QueqiaoConfig: %v", err)
	}
	if qCfg.HopPortCount != 8 || qCfg.Congestion != "bbr" || !qCfg.QuicPool {
		t.Fatalf("QueqiaoConfig stream fields improperly parsed")
	}

	outboundRaw := `{
		"address": "1.2.3.4",
		"port": 443,
		"providerId": "provider.example",
		"gatewayId": "gw-01",
		"accountId": "alice",
		"deviceId": "dev-01",
		"deviceName": "laptop",
		"rootPin": "pin-1234"
	}`
	var cliCfg conf.QueqiaoClientConfig
	if err := json.Unmarshal([]byte(outboundRaw), &cliCfg); err != nil {
		t.Fatalf("Failed to unmarshal QueqiaoClientConfig: %v", err)
	}
	if cliCfg.AccountID != "alice" || cliCfg.DeviceID != "dev-01" || cliCfg.RootPin != "pin-1234" {
		t.Fatalf("QueqiaoClientConfig fields improperly parsed")
	}
}
