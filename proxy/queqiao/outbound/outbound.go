package outbound

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/proxy/queqiao"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
	libqueqiao "github.com/zhfreal/queqiao"
	"github.com/zhfreal/queqiao/identity"
)

type domainUDPAddr struct {
	addr string
}

func (d domainUDPAddr) Network() string { return "udp" }
func (d domainUDPAddr) String() string  { return d.addr }

type Handler struct {
	client     *libqueqiao.Client
	config     *queqiao.ClientConfig
	dialerMu   sync.RWMutex
	dialer     internet.Dialer
	dialerOnce sync.Once
}

func New(ctx context.Context, config *queqiao.ClientConfig) (*Handler, error) {
	streamSettings := session.StreamSettingsFromContext(ctx)
	var sockopt *internet.SocketConfig
	if memStream, ok := streamSettings.(*internet.MemoryStreamConfig); ok && memStream != nil {
		sockopt = memStream.SocketSettings
		if tc, ok := memStream.ProtocolSettings.(*queqiao.TransportConfig); ok && tc != nil {
			if config.HopPortCount == 0 && tc.HopPortCount > 0 {
				config.HopPortCount = tc.HopPortCount
			}
			if config.CongestionController == "" && tc.Congestion != "" {
				config.CongestionController = tc.Congestion
			}
			if !config.QuicPool && tc.QuicPool {
				config.QuicPool = tc.QuicPool
			}
			if !config.WaitForOpenAck && tc.WaitForOpenAck {
				config.WaitForOpenAck = tc.WaitForOpenAck
			}
			if !config.UdpOverStream && tc.UdpOverStream {
				config.UdpOverStream = tc.UdpOverStream
			}
			if config.TcpFallbackLanes == 0 && tc.TcpFallbackLanes > 0 {
				config.TcpFallbackLanes = tc.TcpFallbackLanes
			}
			if config.HandshakeTimeout == 0 && tc.HandshakeTimeout > 0 {
				config.HandshakeTimeout = tc.HandshakeTimeout
			}
			if config.FlowIdleTimeout == 0 && tc.FlowIdleTimeout > 0 {
				config.FlowIdleTimeout = tc.FlowIdleTimeout
			}
			if config.MaxSessions == 0 && tc.MaxSessions > 0 {
				config.MaxSessions = tc.MaxSessions
			}
			if config.ChunkSize == 0 && tc.ChunkSize > 0 {
				config.ChunkSize = tc.ChunkSize
			}
			if config.Transport == "" && tc.Transport != "" {
				config.Transport = tc.Transport
			}
			if config.FallbackDelay == 0 && tc.FallbackDelay > 0 {
				config.FallbackDelay = tc.FallbackDelay
			}
			if config.FallbackGrace == 0 && tc.FallbackGrace > 0 {
				config.FallbackGrace = tc.FallbackGrace
			}
			if config.UdpCooldown == 0 && tc.UdpCooldown > 0 {
				config.UdpCooldown = tc.UdpCooldown
			}
			if config.UdpFailureThreshold == 0 && tc.UdpFailureThreshold > 0 {
				config.UdpFailureThreshold = tc.UdpFailureThreshold
			}
		}
		if tlsConfig, ok := memStream.SecuritySettings.(*xtls.Config); ok && tlsConfig != nil {
			if config.Sni == "" && tlsConfig.ServerName != "" {
				config.Sni = tlsConfig.ServerName
			}
			for _, cert := range tlsConfig.Certificate {
				if cert.Usage == xtls.Certificate_AUTHORITY_VERIFY {
					if len(cert.Certificate) > 0 && config.RootCertificate == "" {
						config.RootCertificate = string(cert.Certificate)
					}
					if cert.CertificatePath != "" && config.RootCertificatePath == "" {
						config.RootCertificatePath = cert.CertificatePath
					}
				} else {
					if len(cert.Certificate) > 0 && config.DeviceCertificate == "" {
						config.DeviceCertificate = string(cert.Certificate)
					}
					if cert.CertificatePath != "" && config.DeviceCertificatePath == "" {
						config.DeviceCertificatePath = cert.CertificatePath
					}
					if len(cert.Key) > 0 && config.DevicePrivateKey == "" {
						config.DevicePrivateKey = string(cert.Key)
					}
					if cert.KeyPath != "" && config.DevicePrivateKeyPath == "" {
						config.DevicePrivateKeyPath = cert.KeyPath
					}
				}
			}
		}
	}

	rootCert, _ := identity.LoadCertOrKey(config.RootCertificate, config.RootCertificatePath)
	devCert, err := identity.LoadCertOrKey(config.DeviceCertificate, config.DeviceCertificatePath)
	if err != nil {
		return nil, err
	}
	devKey, err := identity.LoadCertOrKey(config.DevicePrivateKey, config.DevicePrivateKeyPath)
	if err != nil {
		return nil, err
	}

	if config == nil || config.Server == nil || config.Server.Address == nil {
		return nil, errors.New("invalid queqiao client configuration: server address is required")
	}
	serverAddr := net.JoinHostPort(config.Server.Address.AsAddress().String(), strconv.Itoa(int(config.Server.Port)))
	h := &Handler{
		config: config,
	}

	fallbackDelay := time.Duration(config.FallbackDelay) * time.Millisecond
	if fallbackDelay <= 0 {
		if config.HandshakeTimeout > 0 {
			derived := time.Duration(config.HandshakeTimeout) * time.Millisecond / 5
			if derived >= 500*time.Millisecond && derived <= 2000*time.Millisecond {
				fallbackDelay = derived
			}
		}
		if fallbackDelay <= 0 {
			fallbackDelay = 1000 * time.Millisecond
		}
	}

	client, err := libqueqiao.NewClient(libqueqiao.ClientConfig{
		ServerAddr:          serverAddr,
		ProviderID:          config.ProviderId,
		GatewayID:           config.GatewayId,
		AccountID:           config.AccountId,
		DeviceID:            config.DeviceId,
		SNI:                 config.Sni,
		RootPin:             config.RootPin,
		RootCert:            rootCert,
		DeviceCert:          devCert,
		DeviceKey:           devKey,
		HopCount:            config.HopPortCount,
		Congestion:          config.CongestionController,
		QuicPool:            config.QuicPool,
		WaitForOpenAck:      config.WaitForOpenAck,
		UDPOverStream:       config.UdpOverStream,
		TCPFallbackLanes:    int(config.TcpFallbackLanes),
		HandshakeTimeout:    time.Duration(config.HandshakeTimeout) * time.Millisecond,
		IdleTimeout:         time.Duration(config.FlowIdleTimeout) * time.Millisecond,
		MaxSessions:         int(config.MaxSessions),
		ChunkSize:           int(config.ChunkSize),
		Transport:           config.Transport,
		FallbackDelay:       fallbackDelay,
		FallbackGrace:       time.Duration(config.FallbackGrace) * time.Millisecond,
		UDPCooldown:         time.Duration(config.UdpCooldown) * time.Millisecond,
		UDPFailureThreshold: int(config.UdpFailureThreshold),
		DialContextFunc: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dest, err := xnet.ParseDestination(network + ":" + addr)
			if err != nil {
				return nil, err
			}
			h.dialerMu.RLock()
			d := h.dialer
			h.dialerMu.RUnlock()
			if d != nil {
				if conn, err := d.Dial(ctx, dest); err == nil {
					return conn, nil
				}
			}
			return internet.DialSystem(ctx, dest, sockopt)
		},
		ListenPacketFunc: func(ctx context.Context, network, addr string) (net.PacketConn, error) {
			return internet.ListenSystemPacket(ctx, &net.UDPAddr{}, sockopt)
		},
	})
	if err != nil {
		return nil, err
	}

	h.client = client
	return h, nil
}

func (h *Handler) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	if dialer != nil {
		h.dialerOnce.Do(func() {
			h.dialerMu.Lock()
			h.dialer = dialer
			h.dialerMu.Unlock()
		})
	}
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]
	if !ob.Target.IsValid() {
		return errors.New("target not specified")
	}
	ob.Name = "queqiao"
	target := ob.Target

	if target.Network == xnet.Network_TCP {
		conn, err := h.client.DialContext(ctx, target.NetAddr())
		if err != nil {
			return errors.New("failed to dial queqiao stream").Base(err)
		}
		defer conn.Close()

		requestDone := func() error {
			return buf.Copy(link.Reader, buf.NewWriter(conn))
		}

		responseDone := func() error {
			return buf.Copy(buf.NewReader(conn), link.Writer)
		}

		responseDoneAndCloseWriter := task.OnSuccess(responseDone, task.Close(link.Writer))
		if err := task.Run(ctx, requestDone, responseDoneAndCloseWriter); err != nil {
			return errors.New("connection ends").Base(err)
		}

		return nil
	}

	pc, err := h.client.ListenPacket(ctx)
	if err != nil {
		return errors.New("failed to open UDP association").Base(err)
	}
	defer pc.Close()

	return relayUDP(ctx, target, link, pc)
}

func relayUDP(ctx context.Context, target xnet.Destination, link *transport.Link, pc net.PacketConn) error {
	var targetAddr net.Addr
	if target.Address.Family().IsIP() {
		targetAddr = &net.UDPAddr{IP: target.Address.IP(), Port: int(target.Port)}
	} else {
		targetAddr = domainUDPAddr{addr: target.NetAddr()}
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = pc.Close()
	}()

	postOutput := func() error {
		defer common.Close(link.Writer)
		for {
			b := buf.New()
			n, rAddr, err := pc.ReadFrom(b.Extend(buf.Size))
			if err != nil {
				b.Release()
				return err
			}
			b.Resize(0, int32(n))
			if udpAddr, ok := rAddr.(*net.UDPAddr); ok {
				b.UDP = &xnet.Destination{
					Address: xnet.IPAddress(udpAddr.IP),
					Port:    xnet.Port(udpAddr.Port),
					Network: xnet.Network_UDP,
				}
			} else if rAddr != nil {
				if d, parseErr := xnet.ParseDestination("udp:" + rAddr.String()); parseErr == nil {
					b.UDP = &d
				}
			}
			if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{b}); err != nil {
				return err
			}
		}
	}

	fetchInput := func() error {
		for {
			mb, err := link.Reader.ReadMultiBuffer()
			if err != nil {
				return err
			}
			for _, b := range mb {
				destAddr := targetAddr
				if b.UDP != nil {
					if b.UDP.Address.Family().IsIP() {
						destAddr = &net.UDPAddr{IP: b.UDP.Address.IP(), Port: int(b.UDP.Port)}
					} else {
						destAddr = domainUDPAddr{addr: b.UDP.NetAddr()}
					}
				}
				if _, err := pc.WriteTo(b.Bytes(), destAddr); err != nil {
					buf.ReleaseMulti(mb)
					return err
				}
			}
			buf.ReleaseMulti(mb)
		}
	}

	return task.Run(ctx, postOutput, fetchInput)
}

func (h *Handler) Close() error {
	if h.client != nil {
		return h.client.Close()
	}
	return nil
}

func DialQueqiao(ctx context.Context, dest xnet.Destination, streamSettings *internet.MemoryStreamConfig) (stat.Connection, error) {
	var sockopt *internet.SocketConfig
	if streamSettings != nil {
		sockopt = streamSettings.SocketSettings
	}
	conn, err := internet.DialSystem(ctx, dest, sockopt)
	if err != nil {
		return nil, err
	}
	return stat.Connection(conn), nil
}

func init() {
	common.Must(common.RegisterConfig((*queqiao.ClientConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*queqiao.ClientConfig))
	}))
	common.Must(internet.RegisterTransportDialer("queqiao", DialQueqiao))
}
