package singmux

import (
	"context"
	"testing"

	xraynet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

func TestStringToBps(t *testing.T) {
	tests := []struct {
		input string
		want  uint64
	}{
		{"", 0},
		{"10", 1250000}, // 10 Mbps = 10 * 1000 * 1000 / 8 = 1250000 bytes/sec
		{"100", 12500000},
		{"10 Mbps", 1250000},
		{"10 Mbps", 1250000},
		{"1 Gbps", 125000000}, // 1 Gbps = 1000 * 1000 * 1000 / 8 = 125,000,000 bytes/sec
		{"100 Kbps", 12500},
		{"8 bps", 1},
		{"invalid", 0},
	}

	for _, tt := range tests {
		got := StringToBps(tt.input)
		if got != tt.want {
			t.Errorf("StringToBps(%q) = %d; want %d", tt.input, got, tt.want)
		}
	}
}

type mockDispatcher struct{}

func (m *mockDispatcher) Type() interface{} {
	return routing.DispatcherType()
}
func (m *mockDispatcher) Start() error { return nil }
func (m *mockDispatcher) Close() error { return nil }
func (m *mockDispatcher) Dispatch(ctx context.Context, dest xraynet.Destination) (*transport.Link, error) {
	return nil, nil
}
func (m *mockDispatcher) DispatchLink(ctx context.Context, dest xraynet.Destination, link *transport.Link) error {
	return nil
}

func TestNewServer(t *testing.T) {
	v := &core.Instance{}
	if err := v.AddFeature(&mockDispatcher{}); err != nil {
		t.Fatalf("AddFeature failed: %v", err)
	}

	ctx := context.WithValue(context.Background(), core.XrayKey(1), v)
	server, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil server")
	}
}

