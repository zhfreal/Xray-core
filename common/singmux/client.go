package singmux

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/metacubex/sing-mux"
	M "github.com/metacubex/sing/common/metadata"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	xraynet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/pipe"
)

type singDialer struct {
	proxy  proxy.Outbound
	dialer internet.Dialer
}

func (d *singDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	opts := []pipe.Option{pipe.WithSizeLimit(64 * 1024)}
	uplinkReader, uplinkWriter := pipe.New(opts...)
	downlinkReader, downlinkWriter := pipe.New(opts...)

	inbound := session.InboundFromContext(ctx)
	content := session.ContentFromContext(ctx)

	go func() {
		outbounds := []*session.Outbound{{
			Target: xraynet.TCPDestination(xraynet.DomainAddress("sp.mux.sing-box.arpa"), xraynet.Port(444)),
		}}
		pCtx := session.ContextWithOutbounds(context.Background(), outbounds)
		if inbound != nil {
			pCtx = session.ContextWithInbound(pCtx, inbound)
		}
		if content != nil {
			pCtx = session.ContextWithContent(pCtx, content)
		}
		pCtx, cancel := context.WithCancel(pCtx)
		defer cancel()
		defer common.Close(uplinkReader)
		defer common.Close(downlinkWriter)
		if err := d.proxy.Process(pCtx, &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}, d.dialer); err != nil {
			errors.LogInfoInner(pCtx, err, "sing-mux dial connection process failed")
		}
	}()

	conn := cnc.NewConnection(
		cnc.ConnectionInputMulti(uplinkWriter),
		cnc.ConnectionOutputMulti(downlinkReader),
	)
	return conn, nil
}

func (d *singDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("UDP listen packet not supported for sing-mux underlay dialer")
}

type SingMuxClientManager struct {
	client *mux.Client
}

func NewSingMuxClientManager(config *proxyman.MultiplexingConfig, p proxy.Outbound, dialer internet.Dialer) (*SingMuxClientManager, error) {
	dialerAdapter := &singDialer{
		proxy:  p,
		dialer: dialer,
	}

	brutalOpts := mux.BrutalOptions{
		Enabled:    config.Brutal,
		SendBPS:    StringToBps(config.BrutalUp),
		ReceiveBPS: StringToBps(config.BrutalDown),
	}

	client, err := mux.NewClient(mux.Options{
		Dialer:         dialerAdapter,
		Protocol:       config.Protocol,
		MaxConnections: int(config.MaxConnections),
		MinStreams:     int(config.MinStreams),
		MaxStreams:     int(config.MaxStreams),
		Padding:          config.Padding,
		Brutal:           brutalOpts,
		CMaxReuseTimes:   config.CMaxReuseTimes,
		HMaxRequestTimes: config.HMaxRequestTimes,
		HMaxReusableSecs: config.HMaxReusableSecs,
	})
	if err != nil {
		return nil, err
	}

	return &SingMuxClientManager{client: client}, nil
}

func (m *SingMuxClientManager) Dispatch(ctx context.Context, link *transport.Link) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		return errors.New("outbound target not found")
	}
	ob := outbounds[len(outbounds)-1]

	var destination M.Socksaddr
	addr := ob.Target.Address
	if addr.Family().IsDomain() {
		destination = M.ParseSocksaddrHostPort(addr.Domain(), uint16(ob.Target.Port))
	} else {
		ip := addr.IP()
		if ob.Target.Network == xraynet.Network_UDP {
			destination = M.SocksaddrFromNet(&net.UDPAddr{
				IP:   ip,
				Port: int(ob.Target.Port),
			})
		} else {
			destination = M.SocksaddrFromNet(&net.TCPAddr{
				IP:   ip,
				Port: int(ob.Target.Port),
			})
		}
	}

	networkStr := "tcp"
	if ob.Target.Network == xraynet.Network_UDP {
		networkStr = "udp"
	}

	// Use context.Background() here to prevent stream closure when the transient request context is cancelled.
	stream, err := m.client.DialContext(context.Background(), networkStr, destination)
	if err != nil {
		errors.LogInfo(ctx, "sing-mux DialContext failed: ", err)
		return err
	}

	// Block synchronously to prevent Xray's dispatcher / inbound handlers from prematurely tearing down the client connection.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer closeReader(link.Reader)
		defer stream.Close()
		err := buf.Copy(buf.NewReader(stream), link.Writer)
		if err != nil {
			errors.LogInfo(context.Background(), "sing-mux client copy server to link err: ", err)
		}
	}()

	go func() {
		defer wg.Done()
		defer common.Close(link.Writer)
		defer stream.Close()
		err := buf.Copy(link.Reader, buf.NewWriter(stream))
		if err != nil {
			errors.LogInfo(context.Background(), "sing-mux client copy link to server err: ", err)
		}
	}()

	wg.Wait()
	return nil
}

func (m *SingMuxClientManager) Close() error {
	if m == nil || m.client == nil {
		return nil
	}
	return m.client.Close()
}

var rateStringRegexp = regexp.MustCompile(`(?i)^(\d+)\s*([kmgt]?)([b])ps$`)

func StringToBps(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	if v, err := strconv.Atoi(s); err == nil {
		return StringToBps(fmt.Sprintf("%d Mbps", v))
	}

	m := rateStringRegexp.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	var n uint64 = 1
	switch strings.ToUpper(m[2]) {
	case "T":
		n *= 1000
		fallthrough
	case "G":
		n *= 1000
		fallthrough
	case "M":
		n *= 1000
		fallthrough
	case "K":
		n *= 1000
	}
	v, _ := strconv.ParseUint(m[1], 10, 64)
	n *= v
	if m[3] == "b" {
		n /= 8
	}
	return n
}

func closeReader(reader interface{}) {
	if reader == nil {
		return
	}
	if c, ok := reader.(io.Closer); ok {
		_ = c.Close()
		return
	}
	if c, ok := reader.(common.Interruptible); ok {
		c.Interrupt()
		return
	}

	switch r := reader.(type) {
	case *buf.BufferedReader:
		closeReader(r.Reader)
	case *buf.TimeoutWrapperReader:
		closeReader(r.Reader)
	case *buf.ReadVReader:
		closeReader(r.Reader)
	case interface{ Upstream() any }:
		closeReader(r.Upstream())
	case interface{ NetConn() net.Conn }:
		closeReader(r.NetConn())
	}
}

