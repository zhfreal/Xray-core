package inbound

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy/queqiao"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
	libqueqiao "github.com/zhfreal/lib-queqiao"
	"github.com/zhfreal/lib-queqiao/identity"
)

type Handler struct {
	server          *libqueqiao.Server
	config          *queqiao.ServerConfig
	tag             string
	listenDest      xnet.Destination
	sniffingRequest session.SniffingRequest
	dispatcher      routing.Dispatcher
	policyManager   policy.Manager
	users           sync.Map
}

func New(ctx context.Context, config *queqiao.ServerConfig) (*Handler, error) {
	v := core.MustFromContext(ctx)
	p := v.GetFeature(policy.ManagerType()).(policy.Manager)
	d := v.GetFeature(routing.DispatcherType()).(routing.Dispatcher)
	inbound := session.InboundFromContext(ctx)
	content := session.ContentFromContext(ctx)

	var sniffingRequest session.SniffingRequest
	if content != nil {
		sniffingRequest = content.SniffingRequest
	}
	var tag string
	var listenDest xnet.Destination
	if inbound != nil {
		tag = inbound.Tag
		if inbound.Gateway.IsValid() && inbound.Gateway.Port > 0 {
			listenDest = inbound.Gateway
		} else if inbound.Source.IsValid() && inbound.Source.Port > 0 {
			listenDest = inbound.Source
		}
	}

	h := &Handler{
		config:          config,
		tag:             tag,
		listenDest:      listenDest,
		sniffingRequest: sniffingRequest,
		dispatcher:      d,
		policyManager:   p,
	}

	rootCertStr := config.RootCertificate
	rootCertPath := config.RootCertificatePath
	rootKeyStr := config.RootPrivateKey
	rootKeyPath := config.RootPrivateKeyPath
	gwCertStr := config.Certificate
	gwCertPath := config.CertificatePath
	gwKeyStr := config.PrivateKey
	gwKeyPath := config.PrivateKeyPath
	hopCount := config.HopPortCount
	congestion := config.Congestion
	maxSessions := int(config.MaxSessions)
	var idleTimeout time.Duration
	if config.FlowIdleTimeout > 0 {
		idleTimeout = time.Duration(config.FlowIdleTimeout) * time.Millisecond
	}
	var handshakeTimeout time.Duration

	streamSettings := session.StreamSettingsFromContext(ctx)
	if streamSettings != nil {
		if memStream, ok := streamSettings.(*internet.MemoryStreamConfig); ok && memStream != nil {
			if tc, ok := memStream.ProtocolSettings.(*queqiao.TransportConfig); ok && tc != nil {
				if hopCount == 0 && tc.HopPortCount > 0 {
					hopCount = tc.HopPortCount
				}
				if congestion == "" && tc.Congestion != "" {
					congestion = tc.Congestion
				}
				if maxSessions == 0 && tc.MaxSessions > 0 {
					maxSessions = int(tc.MaxSessions)
				}
				if tc.HandshakeTimeout > 0 {
					handshakeTimeout = time.Duration(tc.HandshakeTimeout) * time.Millisecond
				}
				if idleTimeout == 0 && tc.FlowIdleTimeout > 0 {
					idleTimeout = time.Duration(tc.FlowIdleTimeout) * time.Millisecond
				}
			}
			if tlsConfig, ok := memStream.SecuritySettings.(*xtls.Config); ok && tlsConfig != nil {
				for _, cert := range tlsConfig.Certificate {
					if cert.Usage == xtls.Certificate_AUTHORITY_VERIFY {
						if len(cert.Certificate) > 0 && rootCertStr == "" {
							rootCertStr = string(cert.Certificate)
						}
						if cert.CertificatePath != "" && rootCertPath == "" {
							rootCertPath = cert.CertificatePath
						}
					} else {
						if len(cert.Certificate) > 0 && gwCertStr == "" {
							gwCertStr = string(cert.Certificate)
						}
						if cert.CertificatePath != "" && gwCertPath == "" {
							gwCertPath = cert.CertificatePath
						}
						if len(cert.Key) > 0 && gwKeyStr == "" {
							gwKeyStr = string(cert.Key)
						}
						if cert.KeyPath != "" && gwKeyPath == "" {
							gwKeyPath = cert.KeyPath
						}
					}
				}
			}
		}
	}

	rootCert, _ := identity.LoadCertOrKey(rootCertStr, rootCertPath)
	rootKey, _ := identity.LoadCertOrKey(rootKeyStr, rootKeyPath)
	gwCert, err := identity.LoadCertOrKey(gwCertStr, gwCertPath)
	if err != nil {
		return nil, err
	}
	gwKey, err := identity.LoadCertOrKey(gwKeyStr, gwKeyPath)
	if err != nil {
		return nil, err
	}

	server, err := libqueqiao.NewServer(libqueqiao.ServerConfig{
		ListenAddr:       listenDest.NetAddr(),
		ProviderID:       config.ProviderId,
		GatewayID:        config.GatewayId,
		RootPin:          config.RootPin,
		RootCert:         rootCert,
		RootKey:          rootKey,
		GatewayCert:      gwCert,
		GatewayKey:       gwKey,
		HopCount:         hopCount,
		Congestion:       congestion,
		MaxSessions:      maxSessions,
		HandshakeTimeout: handshakeTimeout,
		IdleTimeout:      idleTimeout,
		AllowPrivate:     config.AllowPrivate,
		StreamHandler: func(ctx context.Context, clientAddr net.Addr, dest string, stream net.Conn, principal identity.Principal) error {
			user := &protocol.MemoryUser{Email: principal.AccountID + "/" + principal.DeviceID}
			targetDest, err := xnet.ParseDestination("tcp:" + dest)
			if err != nil {
				return err
			}
			ctx = session.ContextWithInbound(ctx, &session.Inbound{
				Source:  xnet.DestinationFromAddr(clientAddr),
				Gateway: h.listenDest,
				Tag:     h.tag,
				User:    user,
			})
			ctx = session.ContextWithContent(ctx, &session.Content{
				SniffingRequest: h.sniffingRequest,
			})
			link := &transport.Link{
				Reader: &buf.TimeoutWrapperReader{Reader: buf.NewReader(stream)},
				Writer: buf.NewWriter(stream),
			}
			return h.dispatcher.DispatchLink(ctx, targetDest, link)
		},
		PacketHandler: func(ctx context.Context, clientAddr net.Addr, pc net.PacketConn, principal identity.Principal) error {
			user := &protocol.MemoryUser{Email: principal.AccountID + "/" + principal.DeviceID}
			udpCtx := session.ContextWithInbound(ctx, &session.Inbound{
				Source:  xnet.DestinationFromAddr(clientAddr),
				Gateway: h.listenDest,
				Tag:     h.tag,
				User:    user,
			})
			udpCtx = session.ContextWithContent(udpCtx, &session.Content{
				SniffingRequest: h.sniffingRequest,
			})
			go func() {
				defer pc.Close()
				for {
					b := buf.New()
					n, rAddr, err := pc.ReadFrom(b.Extend(buf.Size))
					if err != nil {
						b.Release()
						return
					}
					b.Resize(0, int32(n))
					var dest xnet.Destination
					if uAddr, ok := rAddr.(*net.UDPAddr); ok {
						dest = xnet.UDPDestination(xnet.IPAddress(uAddr.IP), xnet.Port(uAddr.Port))
					} else {
						d, parseErr := xnet.ParseDestination("udp:" + rAddr.String())
						if parseErr != nil {
							b.Release()
							continue
						}
						dest = d
					}
					link := &transport.Link{
						Reader: &oneShotReader{b: b},
						Writer: &packetBackWriter{pc: pc, target: rAddr},
					}
					go func(d xnet.Destination, l *transport.Link) {
						_ = h.dispatcher.DispatchLink(udpCtx, d, l)
					}(dest, link)
				}
			}()
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	h.server = server

	for _, acc := range config.Accounts {
		for _, dev := range acc.Devices {
			email := acc.AccountId + "/" + dev.DeviceId
			h.users.Store(email, &protocol.MemoryUser{Email: email})
			if pubKey, err := identity.DecodeEd25519PublicKey(dev.PublicKey); err == nil {
				_ = h.server.AddUser(acc.AccountId, dev.DeviceId, pubKey)
			}
		}
	}

	return h, nil
}

func (h *Handler) Network() []xnet.Network {
	return []xnet.Network{}
}

func (h *Handler) Process(ctx context.Context, network xnet.Network, conn stat.Connection, dispatcher routing.Dispatcher) error {
	return nil
}

func (h *Handler) Start() error {
	go func() {
		_ = h.server.Start()
	}()
	return nil
}

func (h *Handler) Close() error {
	if h.server != nil {
		return h.server.Close()
	}
	return nil
}

func (h *Handler) AddUser(ctx context.Context, u *protocol.MemoryUser) error {
	h.users.Store(u.Email, u)
	if h.server != nil {
		if memAcc, ok := u.Account.(*queqiao.MemoryAccount); ok && memAcc != nil {
			for _, d := range memAcc.Devices {
				pub, err := identity.DecodeEd25519PublicKey(d.PublicKey)
				if err == nil {
					_ = h.server.AddUser(memAcc.AccountID, d.DeviceId, pub)
				}
			}
		} else if rawAcc, ok := u.Account.(*queqiao.Account); ok && rawAcc != nil {
			for _, d := range rawAcc.Devices {
				pub, err := identity.DecodeEd25519PublicKey(d.PublicKey)
				if err == nil {
					_ = h.server.AddUser(rawAcc.AccountId, d.DeviceId, pub)
				}
			}
		}
	}
	return nil
}

func (h *Handler) RemoveUser(ctx context.Context, email string) error {
	h.users.Delete(email)
	if h.server != nil {
		_ = h.server.RevokeUser(email)
	}
	return nil
}

func (h *Handler) GetUser(ctx context.Context, email string) *protocol.MemoryUser {
	if val, ok := h.users.Load(email); ok {
		return val.(*protocol.MemoryUser)
	}
	return nil
}

func (h *Handler) GetUsers(ctx context.Context) (users []*protocol.MemoryUser) {
	h.users.Range(func(key, val any) bool {
		users = append(users, val.(*protocol.MemoryUser))
		return true
	})
	return
}

func (h *Handler) GetUsersCount(ctx context.Context) int64 {
	var count int64
	h.users.Range(func(key, val any) bool {
		count++
		return true
	})
	return count
}

type packetBackWriter struct {
	pc     net.PacketConn
	target net.Addr
}

func (w *packetBackWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	defer buf.ReleaseMulti(mb)
	for _, b := range mb {
		if _, err := w.pc.WriteTo(b.Bytes(), w.target); err != nil {
			return err
		}
	}
	return nil
}

func (w *packetBackWriter) Close() error { return nil }

// oneShotReader yields a single pooled buffer exactly once, then returns EOF.
// Unlike buf.SingleReader (which wraps io.Reader and copies into a new buffer),
// this hands the original buffer directly to the dispatcher without leaking it.
type oneShotReader struct {
	b *buf.Buffer
}

func (r *oneShotReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.b == nil {
		return nil, io.EOF
	}
	b := r.b
	r.b = nil
	return buf.MultiBuffer{b}, nil
}

func init() {
	common.Must(common.RegisterConfig((*queqiao.ServerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*queqiao.ServerConfig))
	}))
}
