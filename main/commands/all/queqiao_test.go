package all_test

import (
	"strings"
	"testing"

	"github.com/zhfreal/lib-queqiao/configgen"
)

func TestXrayQueqiaoFormat(t *testing.T) {
	bundle, err := configgen.GenerateBundle("provider.example", "default", "dev-01", "default")
	if err != nil {
		t.Fatalf("GenerateBundle failed: %v", err)
	}
	if !strings.HasPrefix(bundle.RootCertPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("RootCertPEM missing PEM header")
	}
	if !strings.HasPrefix(bundle.GatewayKeyPEM, "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("GatewayKeyPEM missing PEM header")
	}
	if !strings.HasPrefix(bundle.DeviceCertPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("DeviceCertPEM missing PEM header")
	}
	if len(bundle.RootPin) == 0 {
		t.Errorf("RootPin empty")
	}
}
