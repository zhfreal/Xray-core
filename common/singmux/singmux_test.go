package singmux

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	singbuf "github.com/metacubex/sing/common/buf"
	M "github.com/metacubex/sing/common/metadata"
	xbuf "github.com/xtls/xray-core/common/buf"
	xraynet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
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

type testDispatcherWithLink struct {
	mockDispatcher
	link *transport.Link
}

func (m *testDispatcherWithLink) Dispatch(ctx context.Context, dest xraynet.Destination) (*transport.Link, error) {
	return m.link, nil
}

type mockPacketConn struct {
	packetsToWrite chan *singbuf.Buffer
	closed         chan struct{}
	closeOnce      sync.Once
}

func newMockPacketConn() *mockPacketConn {
	return &mockPacketConn{
		packetsToWrite: make(chan *singbuf.Buffer, 10),
		closed:         make(chan struct{}),
	}
}

func (m *mockPacketConn) ReadPacket(buffer *singbuf.Buffer) (M.Socksaddr, error) {
	<-m.closed
	return M.Socksaddr{}, net.ErrClosed
}

func (m *mockPacketConn) WritePacket(buffer *singbuf.Buffer, destination M.Socksaddr) error {
	m.packetsToWrite <- buffer
	return nil
}

func (m *mockPacketConn) Close() error {
	m.closeOnce.Do(func() {
		close(m.closed)
	})
	return nil
}

func (m *mockPacketConn) LocalAddr() net.Addr                { return nil }
func (m *mockPacketConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error { return nil }

func TestNewPacketConnectionHeadroom(t *testing.T) {
	inReader, inWriter := pipe.New(pipe.WithSizeLimit(1024 * 1024))
	outReader, outWriter := pipe.New(pipe.WithSizeLimit(1024 * 1024))
	defer inReader.Interrupt()
	defer outWriter.Close()

	disp := &testDispatcherWithLink{
		link: &transport.Link{
			Reader: outReader,
			Writer: inWriter,
		},
	}

	mockConn := newMockPacketConn()
	handler := &serviceHandler{
		dispatcher: disp,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- handler.NewPacketConnection(ctx, mockConn, M.Metadata{
			Destination: M.ParseSocksaddr("1.1.1.1:53"),
		})
	}()

	// Simulate outbound returning a UDP response packet through link.Reader (outWriter)
	testPayload := []byte("dns response test payload")
	xBuf := xbuf.New()
	xBuf.Write(testPayload)
	if err := outWriter.WriteMultiBuffer(xbuf.MultiBuffer{xBuf}); err != nil {
		t.Fatalf("WriteMultiBuffer failed: %v", err)
	}

	// Wait for mockConn to receive the packet
	select {
	case receivedBuf := <-mockConn.packetsToWrite:
		// Verify that buffer has sufficient front headroom (at least 256 bytes)
		if receivedBuf.Start() < 256 {
			t.Errorf("expected buffer Start() >= 256, got %d", receivedBuf.Start())
		}
		// Verify that ExtendHeader(2) does not panic (as sing-mux serverPacketConn does)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ExtendHeader(2) panicked: %v", r)
				}
			}()
			receivedBuf.ExtendHeader(2)
		}()
		// Verify payload
		if !bytes.Equal(receivedBuf.Bytes()[2:], testPayload) {
			t.Errorf("payload mismatch, got %s, want %s", string(receivedBuf.Bytes()[2:]), string(testPayload))
		}
		receivedBuf.Release()
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for WritePacket")
	}

	// Unblock and cleanup
	mockConn.Close()
	outWriter.Close()
	inReader.Interrupt()

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NewPacketConnection to return")
	}
}

