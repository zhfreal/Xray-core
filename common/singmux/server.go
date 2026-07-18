package singmux

import (
	"context"
	"net"
	"sync"

	"github.com/metacubex/sing-mux"
	singbuf "github.com/metacubex/sing/common/buf"
	M "github.com/metacubex/sing/common/metadata"
	N "github.com/metacubex/sing/common/network"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	xraynet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	xraymux "github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type Server struct {
	v2rayMux *xraymux.Server
	singMux  *mux.Service
}

func NewServer(ctx context.Context, dispatcher routing.Dispatcher) (*Server, error) {
	v2rayMux := xraymux.NewServer(ctx)

	s := &Server{
		v2rayMux: v2rayMux,
	}

	handler := &serviceHandler{
		dispatcher: dispatcher,
	}

	singMux, err := mux.NewService(mux.ServiceOptions{
		NewStreamContext: func(ctx context.Context, conn net.Conn) context.Context {
			return ctx
		},
		Logger:  &xrayLogger{},
		Handler: handler,
		Padding: false,
	})
	if err != nil {
		return nil, err
	}

	s.singMux = singMux
	return s, nil
}

func (s *Server) Dispatch(ctx context.Context, dest xraynet.Destination) (*transport.Link, error) {
	if dest.Address.Family().IsDomain() {
		domain := dest.Address.Domain()
		if domain == "sp.mux.sing-box.arpa" {
			opts := pipe.OptionsFromContext(ctx)
			uplinkReader, uplinkWriter := pipe.New(opts...)
			downlinkReader, downlinkWriter := pipe.New(opts...)

			conn := cnc.NewConnection(
				cnc.ConnectionInputMulti(downlinkWriter),
				cnc.ConnectionOutputMulti(uplinkReader),
			)

			metadata := M.Metadata{
				Destination: M.ParseSocksaddrHostPort(domain, uint16(dest.Port)),
			}

			go func() {
				if err := s.singMux.NewConnection(ctx, conn, metadata); err != nil {
					errors.LogInfoInner(ctx, err, "sing-mux connection handler failed")
				}
			}()

			return &transport.Link{Reader: downlinkReader, Writer: uplinkWriter}, nil
		}
	}

	return s.v2rayMux.Dispatch(ctx, dest)
}

func (s *Server) DispatchLink(ctx context.Context, dest xraynet.Destination, link *transport.Link) error {
	if dest.Address.Family().IsDomain() {
		domain := dest.Address.Domain()
		if domain == "sp.mux.sing-box.arpa" {
			conn := cnc.NewConnection(
				cnc.ConnectionInputMulti(link.Writer),
				cnc.ConnectionOutputMulti(link.Reader),
			)

			metadata := M.Metadata{
				Destination: M.ParseSocksaddrHostPort(domain, uint16(dest.Port)),
			}

			return s.singMux.NewConnection(ctx, conn, metadata)
		}
	}

	return s.v2rayMux.DispatchLink(ctx, dest, link)
}

func (s *Server) Start() error {
	return s.v2rayMux.Start()
}

func (s *Server) Close() error {
	if s == nil || s.v2rayMux == nil {
		return nil
	}
	return s.v2rayMux.Close()
}

func (s *Server) Type() interface{} {
	return s.v2rayMux.Type()
}

type serviceHandler struct {
	dispatcher routing.Dispatcher
}

func (h *serviceHandler) NewConnection(ctx context.Context, conn net.Conn, metadata M.Metadata) error {
	if inbound := session.InboundFromContext(ctx); inbound != nil {
		inbound.Name = "singmux"
	}

	destAddr := metadata.Destination
	dest := xraynet.TCPDestination(xraynet.ParseAddress(destAddr.AddrString()), xraynet.Port(destAddr.Port))

	link, err := h.dispatcher.Dispatch(ctx, dest)
	if err != nil {
		return err
	}

	// Block synchronously here to prevent sing-mux from closing the underlay stream when NewConnection returns.
	defer conn.Close()
	defer common.Close(link.Writer)
	defer common.Interrupt(link.Reader)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		err := buf.Copy(buf.NewReader(conn), link.Writer)
		if err != nil {
			errors.LogInfo(context.Background(), "sing-mux copy client to target err: ", err)
		}
	}()

	go func() {
		defer wg.Done()
		err := buf.Copy(link.Reader, buf.NewWriter(conn))
		if err != nil {
			errors.LogInfo(context.Background(), "sing-mux copy target to client err: ", err)
		}
	}()

	wg.Wait()
	return nil
}

func (h *serviceHandler) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata M.Metadata) error {
	if inbound := session.InboundFromContext(ctx); inbound != nil {
		inbound.Name = "singmux"
	}

	destAddr := metadata.Destination
	dest := xraynet.UDPDestination(xraynet.ParseAddress(destAddr.AddrString()), xraynet.Port(destAddr.Port))

	link, err := h.dispatcher.Dispatch(ctx, dest)
	if err != nil {
		return err
	}

	// Block synchronously here to prevent sing-mux from closing the underlay packet stream when NewPacketConnection returns.
	defer conn.Close()
	defer common.Close(link.Writer)
	defer common.Interrupt(link.Reader)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Read packets from sing PacketConn and write to Xray link.Writer
		for {
			singBuf := singbuf.NewPacket()
			_, err := conn.ReadPacket(singBuf)
			if err != nil {
				singBuf.Release()
				break
			}
			xrayBuf := buf.New()
			_, _ = xrayBuf.Write(singBuf.Bytes())
			singBuf.Release()

			if err := link.Writer.WriteMultiBuffer(buf.MultiBuffer{xrayBuf}); err != nil {
				break
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Read from Xray link.Reader and write to sing PacketConn
		for {
			multiBuffer, err := link.Reader.ReadMultiBuffer()
			if err != nil {
				break
			}
			for _, xrayBuf := range multiBuffer {
				singBuf := singbuf.NewSize(int(xrayBuf.Len()))
				_, _ = singBuf.Write(xrayBuf.Bytes())
				xrayBuf.Release()

				err := conn.WritePacket(singBuf, destAddr)
				if err != nil {
					singBuf.Release()
					break
				}
			}
		}
	}()

	wg.Wait()
	return nil
}

type xrayLogger struct{}

func (l *xrayLogger) Debug(common ...interface{}) {
	errors.LogDebug(context.Background(), common...)
}

func (l *xrayLogger) Info(common ...interface{}) {
	errors.LogInfo(context.Background(), common...)
}

func (l *xrayLogger) Warn(common ...interface{}) {
	errors.LogWarning(context.Background(), common...)
}

func (l *xrayLogger) Error(common ...interface{}) {
	errors.LogWarning(context.Background(), common...)
}

func (l *xrayLogger) Fatal(common ...interface{}) {
	errors.LogWarning(context.Background(), common...)
}

func (l *xrayLogger) Panic(common ...interface{}) {
	errors.LogWarning(context.Background(), common...)
}

func (l *xrayLogger) Trace(common ...interface{}) {
	errors.LogDebug(context.Background(), common...)
}

func (l *xrayLogger) DebugContext(ctx context.Context, common ...interface{}) {
	errors.LogDebug(ctx, common...)
}

func (l *xrayLogger) InfoContext(ctx context.Context, common ...interface{}) {
	errors.LogInfo(ctx, common...)
}

func (l *xrayLogger) WarnContext(ctx context.Context, common ...interface{}) {
	errors.LogWarning(ctx, common...)
}

func (l *xrayLogger) ErrorContext(ctx context.Context, common ...interface{}) {
	errors.LogWarning(ctx, common...)
}

func (l *xrayLogger) FatalContext(ctx context.Context, common ...interface{}) {
	errors.LogWarning(ctx, common...)
}

func (l *xrayLogger) PanicContext(ctx context.Context, common ...interface{}) {
	errors.LogWarning(ctx, common...)
}

func (l *xrayLogger) TraceContext(ctx context.Context, common ...interface{}) {
	errors.LogDebug(ctx, common...)
}
