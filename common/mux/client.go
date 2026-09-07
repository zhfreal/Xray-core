package mux

import (
	"context"
	goerrors "errors"
	"io"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal/done"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/common/xudp"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/pipe"
)

type ClientManager struct {
	Enabled bool // whether mux is enabled from user config
	Picker  WorkerPicker
}

func (m *ClientManager) Dispatch(ctx context.Context, link *transport.Link) error {
	for i := 0; i < 16; i++ {
		worker, err := m.Picker.PickAvailable()
		if err != nil {
			return err
		}
		if worker.Dispatch(ctx, link) {
			return nil
		}
	}

	return errors.New("unable to find an available mux client").AtWarning()
}

type WorkerPicker interface {
	PickAvailable() (*ClientWorker, error)
}

type IncrementalWorkerPicker struct {
	Factory ClientWorkerFactory

	access          sync.Mutex
	workers         []*ClientWorker
	drainingWorkers []*ClientWorker
	cleanupTask     *task.Periodic
}

func (p *IncrementalWorkerPicker) cleanupFunc() error {
	p.access.Lock()
	defer p.access.Unlock()

	if len(p.workers) == 0 && len(p.drainingWorkers) == 0 {
		return errors.New("no worker")
	}

	p.cleanup()
	return nil
}

func (p *IncrementalWorkerPicker) cleanup() {
	now := time.Now()
	dn := 0
	for _, w := range p.drainingWorkers {
		if w.Closed() {
			continue
		}
		if w.ActiveConnections() == 0 && w.inFlight.Load() == 0 {
			errors.LogDebug(context.Background(), "mux: closing draining worker, ActiveConnections=0")
			_ = w.Close()
			continue
		}
		p.drainingWorkers[dn] = w
		dn++
	}
	for i := dn; i < len(p.drainingWorkers); i++ {
		p.drainingWorkers[i] = nil
	}
	p.drainingWorkers = p.drainingWorkers[:dn]

	n := 0
	for _, w := range p.workers {
		if !w.Closed() {
			if w.IsRetired(now) {
				if w.ActiveConnections() == 0 && w.inFlight.Load() == 0 {
					errors.LogDebug(context.Background(), "mux: closing retired worker, ActiveConnections=0")
					_ = w.Close()
					continue
				}
				p.drainingWorkers = append(p.drainingWorkers, w)
				continue
			}
			p.workers[n] = w
			n++
		}
	}
	for i := n; i < len(p.workers); i++ {
		p.workers[i] = nil
	}
	p.workers = p.workers[:n]
}

func (p *IncrementalWorkerPicker) findAvailable() int {
	now := time.Now()
	for idx, w := range p.workers {
		if !w.IsFull() && !w.IsRetired(now) {
			return idx
		}
	}

	return -1
}

func (p *IncrementalWorkerPicker) pickInternal() (*ClientWorker, bool, error) {
	p.access.Lock()
	defer p.access.Unlock()

	idx := p.findAvailable()
	if idx >= 0 {
		worker := p.workers[idx]
		n := len(p.workers)
		if n > 1 && idx != n-1 {
			p.workers[n-1], p.workers[idx] = p.workers[idx], p.workers[n-1]
		}
		if worker.leftReuseTimes.Load() > 0 {
			worker.leftReuseTimes.Add(-1)
		}
		worker.inFlight.Add(1)
		return worker, false, nil
	}

	// Evict retired workers before creating a new one, so a retired-but-not-yet-cleaned
	// worker doesn't block new worker creation.
	p.cleanup()

	worker, err := p.Factory.Create()
	if err != nil {
		return nil, false, err
	}
	p.workers = append(p.workers, worker)

	if p.cleanupTask == nil {
		p.cleanupTask = &task.Periodic{
			Interval: time.Second * 30,
			Execute:  p.cleanupFunc,
		}
	}

	return worker, true, nil
}

func (p *IncrementalWorkerPicker) PickAvailable() (*ClientWorker, error) {
	worker, start, err := p.pickInternal()
	if err != nil {
		return nil, err
	}
	if start {
		worker.inFlight.Add(1)
		common.Must(p.cleanupTask.Start())
	}

	return worker, nil
}

type ClientWorkerFactory interface {
	Create() (*ClientWorker, error)
}

type DialingWorkerFactory struct {
	Proxy    proxy.Outbound
	Dialer   internet.Dialer
	Strategy ClientStrategy
}

func (f *DialingWorkerFactory) Create() (*ClientWorker, error) {
	opts := []pipe.Option{pipe.WithSizeLimit(64 * 1024)}
	uplinkReader, upLinkWriter := pipe.New(opts...)
	downlinkReader, downlinkWriter := pipe.New(opts...)

	c, err := NewClientWorker(transport.Link{
		Reader: downlinkReader,
		Writer: upLinkWriter,
	}, f.Strategy)
	if err != nil {
		return nil, err
	}

	go func(p proxy.Outbound, d internet.Dialer, c common.Closable) {
		outbounds := []*session.Outbound{{
			Target: net.TCPDestination(muxCoolAddress, muxCoolPort),
		}}
		ctx := session.ContextWithOutbounds(context.Background(), outbounds)
		ctx, cancel := context.WithCancel(ctx)

		if errP := p.Process(ctx, &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}, d); errP != nil {
			errC := errors.Cause(errP)
			if !(goerrors.Is(errC, io.EOF) || goerrors.Is(errC, io.ErrClosedPipe) || goerrors.Is(errC, context.Canceled)) {
				errors.LogInfoInner(ctx, errP, "failed to handler mux client connection")
			}
		}
		common.Must(c.Close())
		cancel()
	}(f.Proxy, f.Dialer, c.done)

	return c, nil
}

type ClientStrategy struct {
	MaxConcurrency   uint32
	MaxConnection    uint32
	CMaxReuseTimes   string
	HMaxRequestTimes string
	HMaxReusableSecs string
}

type ClientWorker struct {
	sessionManager *SessionManager
	link           transport.Link
	done           *done.Instance
	timer          *time.Ticker
	strategy       ClientStrategy
	createdAt      time.Time
	unreusableAt   time.Time
	retiredAt      time.Time
	leftRequests   atomic.Int32
	leftReuseTimes atomic.Int32
	inFlight       atomic.Int32
	retired        atomic.Bool
}

var (
	muxCoolAddress = net.DomainAddress("v1.mux.cool")
	muxCoolPort    = net.Port(9527)
)

// NewClientWorker creates a new mux.Client.
func NewClientWorker(stream transport.Link, s ClientStrategy) (*ClientWorker, error) {
	c := &ClientWorker{
		sessionManager: NewSessionManager(),
		link:           stream,
		done:           done.New(),
		timer:          time.NewTicker(time.Second * 16),
		strategy:       s,
		createdAt:      time.Now(),
	}
	if minVal, maxVal, err := parseRangeString(s.HMaxReusableSecs); err == nil && maxVal > 0 {
		if sec := minVal + rand.Intn(maxVal-minVal+1); sec > 0 {
			c.unreusableAt = c.createdAt.Add(time.Duration(sec) * time.Second)
		}
	}
	c.leftRequests.Store(math.MaxInt32)
	if minVal, maxVal, err := parseRangeString(s.HMaxRequestTimes); err == nil && maxVal > 0 {
		if req := minVal + rand.Intn(maxVal-minVal+1); req > 0 {
			c.leftRequests.Store(int32(req))
		}
	}
	c.leftReuseTimes.Store(-1)
	if minVal, maxVal, err := parseRangeString(s.CMaxReuseTimes); err == nil && maxVal > 0 {
		if reuse := minVal + rand.Intn(maxVal-minVal+1); reuse > 0 {
			c.leftReuseTimes.Store(int32(reuse))
		}
	}

	go c.fetchOutput()
	go c.monitor()

	return c, nil
}

func (m *ClientWorker) IsRetired(now time.Time) bool {
	if m.Closed() || m.retired.Load() {
		return true
	}
	if !m.unreusableAt.IsZero() && now.After(m.unreusableAt) {
		m.retired.Store(true)
		return true
	}
	if m.leftRequests.Load() <= 0 {
		m.retired.Store(true)
		return true
	}
	if m.leftReuseTimes.Load() == 0 {
		return true
	}
	return false
}

// parseRangeString parses a range string like "1800-3600" or plain "1800".
// Returns (min, max, error). For plain integers, min == max.
// Handles whitespace and inverts bounds if min > max to prevent rand.Intn panic.
// This is a local copy to avoid importing infra/conf (circular dependency).
func parseRangeString(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	if v, err := strconv.Atoi(s); err == nil {
		if v < 0 {
			return 0, 0, goerrors.New("negative range: " + s)
		}
		return v, v, nil
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) == 2 {
		left, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		right, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err == nil && err2 == nil {
			if left < 0 || right < 0 {
				return 0, 0, goerrors.New("negative range: " + s)
			}
			if left > right {
				left, right = right, left
			}
			return left, right, nil
		}
	}
	return 0, 0, goerrors.New("invalid range string: " + s)
}

func (m *ClientWorker) TotalConnections() uint32 {
	return uint32(m.sessionManager.Count())
}

func (m *ClientWorker) ActiveConnections() uint32 {
	return uint32(m.sessionManager.Size())
}

// Closed returns true if this Client is closed.
func (m *ClientWorker) Closed() bool {
	return m.done.Done()
}

func (m *ClientWorker) WaitClosed() <-chan struct{} {
	return m.done.Wait()
}

func (m *ClientWorker) Close() error {
	return m.done.Close()
}

func (m *ClientWorker) monitor() {
	defer m.timer.Stop()

	for {
		checkSize := m.sessionManager.Size()
		checkCount := m.sessionManager.Count()
		select {
		case <-m.done.Wait():
			m.sessionManager.Close()
			common.Interrupt(m.link.Writer)
			common.Interrupt(m.link.Reader)
			return
		case <-m.timer.C:
			now := time.Now()
			if m.IsRetired(now) {
				if m.sessionManager.Size() == 0 && m.inFlight.Load() == 0 {
					errors.LogDebug(context.Background(), "mux: retired worker draining complete, closing")
					m.sessionManager.Close()
					common.Interrupt(m.link.Writer)
					common.Interrupt(m.link.Reader)
					common.Must(m.done.Close())
					return
				}
				if m.retiredAt.IsZero() {
					m.retiredAt = now
				} else if now.Sub(m.retiredAt) > 5*time.Minute {
					errors.LogWarning(context.Background(), "mux: retired worker exceeded hard drain timeout (5m), force closing")
					m.sessionManager.Close()
					common.Interrupt(m.link.Writer)
					common.Interrupt(m.link.Reader)
					common.Must(m.done.Close())
					return
				}
			}
			if m.sessionManager.CloseIfNoSessionAndIdle(checkSize, checkCount) {
				common.Must(m.done.Close())
			}
		}
	}
}

func writeFirstPayload(reader buf.Reader, writer *Writer) error {
	err := buf.CopyOnceTimeout(reader, writer, time.Millisecond*100)
	if err == buf.ErrNotTimeoutReader || err == buf.ErrReadTimeout {
		return writer.WriteMultiBuffer(buf.MultiBuffer{})
	}

	if err != nil {
		return err
	}

	return nil
}

func fetchInput(ctx context.Context, s *Session, output buf.Writer) {
	outbounds := session.OutboundsFromContext(ctx)
	ob := outbounds[len(outbounds)-1]
	transferType := protocol.TransferTypeStream
	if ob.Target.Network == net.Network_UDP {
		transferType = protocol.TransferTypePacket
	}
	s.transferType = transferType
	var inbound *session.Inbound
	if session.IsReverseMuxFromContext(ctx) {
		inbound = session.InboundFromContext(ctx)
	}
	writer := NewWriter(s.ID, ob.Target, output, transferType, xudp.GetGlobalID(ctx), inbound)
	defer s.Close(false)
	defer writer.Close()

	errors.LogInfo(ctx, "dispatching request to ", ob.Target)
	if err := writeFirstPayload(s.input, writer); err != nil {
		errors.LogInfoInner(ctx, err, "failed to write first payload")
		writer.hasError = true
		return
	}

	if err := buf.Copy(s.input, writer); err != nil {
		errors.LogInfoInner(ctx, err, "failed to fetch all input")
		writer.hasError = true
		return
	}
}

func (m *ClientWorker) IsClosing() bool {
	sm := m.sessionManager
	if m.strategy.MaxConnection > 0 && sm.Count() >= int(m.strategy.MaxConnection) {
		return true
	}
	return false
}

// IsFull returns true if this ClientWorker is unable to accept more connections.
// it might be because it is closing, or the number of connections has reached the limit.
func (m *ClientWorker) IsFull() bool {
	if m.IsClosing() || m.Closed() {
		return true
	}

	sm := m.sessionManager
	if m.strategy.MaxConcurrency > 0 && sm.Size() >= int(m.strategy.MaxConcurrency) {
		return true
	}
	return false
}

func (m *ClientWorker) Dispatch(ctx context.Context, link *transport.Link) bool {
	defer m.inFlight.Add(-1)
	if m.Closed() {
		return false
	}
	now := time.Now()
	if !m.unreusableAt.IsZero() && now.After(m.unreusableAt) {
		m.retired.Store(true)
		return false
	}
	if m.leftRequests.Load() <= 0 {
		m.retired.Store(true)
		return false
	}
	if m.IsFull() {
		return false
	}

	sm := m.sessionManager
	s := sm.Allocate(&m.strategy)
	if s == nil {
		return false
	}
	// Note: Session allocation precedes CAS decrement so that failed allocations do not
	// prematurely consume request quota. In high-concurrency scenarios, up to MaxConcurrency
	// simultaneous callers may pass the leftRequests > 0 check before the CAS loop, which
	// may slightly exceed the nominal quota by at most MaxConcurrency requests. This is a
	// conscious trade-off (matching XMUX) to guarantee zero wasted quota on allocation failure.
	for {
		req := m.leftRequests.Load()
		if req <= 0 {
			m.retired.Store(true)
			break
		}
		if m.leftRequests.CompareAndSwap(req, req-1) {
			if req-1 == 0 {
				m.retired.Store(true)
			}
			break
		}
	}
	s.input = link.Reader
	s.output = link.Writer
	go fetchInput(ctx, s, m.link.Writer)
	if _, ok := link.Reader.(*pipe.Reader); !ok {
		select {
		case <-ctx.Done():
		case <-s.done.Wait():
		}
	}
	return true
}

func (m *ClientWorker) handleStatueKeepAlive(meta *FrameMetadata, reader *buf.BufferedReader) error {
	if meta.Option.Has(OptionData) {
		return buf.Copy(NewStreamReader(reader), buf.Discard)
	}
	return nil
}

func (m *ClientWorker) handleStatusNew(meta *FrameMetadata, reader *buf.BufferedReader) error {
	if meta.Option.Has(OptionData) {
		return buf.Copy(NewStreamReader(reader), buf.Discard)
	}
	return nil
}

func (m *ClientWorker) handleStatusKeep(meta *FrameMetadata, reader *buf.BufferedReader) error {
	if !meta.Option.Has(OptionData) {
		return nil
	}

	s, found := m.sessionManager.Get(meta.SessionID)
	if !found {
		// Notify remote peer to close this session.
		closingWriter := NewResponseWriter(meta.SessionID, m.link.Writer, protocol.TransferTypeStream)
		closingWriter.Close()

		return buf.Copy(NewStreamReader(reader), buf.Discard)
	}

	rr := s.NewReader(reader, &meta.Target)
	err := buf.Copy(rr, s.output)
	if err != nil && buf.IsWriteError(err) {
		errors.LogInfoInner(context.Background(), err, "failed to write to downstream. closing session ", s.ID)
		s.Close(false)
		return buf.Copy(rr, buf.Discard)
	}

	return err
}

func (m *ClientWorker) handleStatusEnd(meta *FrameMetadata, reader *buf.BufferedReader) error {
	if s, found := m.sessionManager.Get(meta.SessionID); found {
		s.Close(false)
	}
	if meta.Option.Has(OptionData) {
		return buf.Copy(NewStreamReader(reader), buf.Discard)
	}
	return nil
}

func (m *ClientWorker) fetchOutput() {
	defer func() {
		common.Must(m.done.Close())
	}()

	reader := &buf.BufferedReader{Reader: m.link.Reader}

	var meta FrameMetadata
	for {
		err := meta.Unmarshal(reader, false)
		if err != nil {
			if errors.Cause(err) != io.EOF {
				errors.LogInfoInner(context.Background(), err, "failed to read metadata")
			}
			break
		}

		switch meta.SessionStatus {
		case SessionStatusKeepAlive:
			err = m.handleStatueKeepAlive(&meta, reader)
		case SessionStatusEnd:
			err = m.handleStatusEnd(&meta, reader)
		case SessionStatusNew:
			err = m.handleStatusNew(&meta, reader)
		case SessionStatusKeep:
			err = m.handleStatusKeep(&meta, reader)
		default:
			status := meta.SessionStatus
			errors.LogError(context.Background(), "unknown status: ", status)
			return
		}

		if err != nil {
			errors.LogInfoInner(context.Background(), err, "failed to process data")
			return
		}
	}
}
