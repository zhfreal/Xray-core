package mux_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/testing/mocks"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestIncrementalPickerFailure(t *testing.T) {
	mockCtl := gomock.NewController(t)
	defer mockCtl.Finish()

	mockWorkerFactory := mocks.NewMuxClientWorkerFactory(mockCtl)
	mockWorkerFactory.EXPECT().Create().Return(nil, errors.New("test"))

	picker := mux.IncrementalWorkerPicker{
		Factory: mockWorkerFactory,
	}

	_, err := picker.PickAvailable()
	if err == nil {
		t.Error("expected error, but nil")
	}
}

func TestClientWorkerEOF(t *testing.T) {
	reader, writer := pipe.New(pipe.WithoutSizeLimit())
	common.Must(writer.Close())

	worker, err := mux.NewClientWorker(transport.Link{Reader: reader, Writer: writer}, mux.ClientStrategy{})
	common.Must(err)

	time.Sleep(time.Millisecond * 500)

	f := worker.Dispatch(context.Background(), nil)
	if f {
		t.Error("expected failed dispatching, but actually not")
	}
}

func TestClientWorkerClose(t *testing.T) {
	mockCtl := gomock.NewController(t)
	defer mockCtl.Finish()

	r1, w1 := pipe.New(pipe.WithoutSizeLimit())
	worker1, err := mux.NewClientWorker(transport.Link{
		Reader: r1,
		Writer: w1,
	}, mux.ClientStrategy{
		MaxConcurrency: 4,
		MaxConnection:  4,
	})
	common.Must(err)

	r2, w2 := pipe.New(pipe.WithoutSizeLimit())
	worker2, err := mux.NewClientWorker(transport.Link{
		Reader: r2,
		Writer: w2,
	}, mux.ClientStrategy{
		MaxConcurrency: 4,
		MaxConnection:  4,
	})
	common.Must(err)

	factory := mocks.NewMuxClientWorkerFactory(mockCtl)
	gomock.InOrder(
		factory.EXPECT().Create().Return(worker1, nil),
		factory.EXPECT().Create().Return(worker2, nil),
	)

	picker := &mux.IncrementalWorkerPicker{
		Factory: factory,
	}
	manager := &mux.ClientManager{
		Picker: picker,
	}

	tr1, tw1 := pipe.New(pipe.WithoutSizeLimit())
	ctx1 := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})
	common.Must(manager.Dispatch(ctx1, &transport.Link{
		Reader: tr1,
		Writer: tw1,
	}))
	defer tw1.Close()

	common.Must(w1.Close())

	time.Sleep(time.Millisecond * 500)
	if !worker1.Closed() {
		t.Error("worker1 is not finished")
	}

	tr2, tw2 := pipe.New(pipe.WithoutSizeLimit())
	ctx2 := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})
	common.Must(manager.Dispatch(ctx2, &transport.Link{
		Reader: tr2,
		Writer: tw2,
	}))
	defer tw2.Close()

	common.Must(w2.Close())
}

func TestClientWorkerRetirementRequests(t *testing.T) {
	r, w := pipe.New(pipe.WithoutSizeLimit())
	defer w.Close()

	worker, err := mux.NewClientWorker(transport.Link{Reader: r, Writer: w}, mux.ClientStrategy{
		MaxConcurrency:   4,
		HMaxRequestTimes: "2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer worker.Close()

	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})

	// Dispatch request 1
	tr1, tw1 := pipe.New(pipe.WithoutSizeLimit())
	defer tw1.Close()
	if !worker.Dispatch(ctx, &transport.Link{Reader: tr1, Writer: tw1}) {
		t.Fatal("expected dispatch 1 to succeed")
	}

	// Dispatch request 2
	tr2, tw2 := pipe.New(pipe.WithoutSizeLimit())
	defer tw2.Close()
	if !worker.Dispatch(ctx, &transport.Link{Reader: tr2, Writer: tw2}) {
		t.Fatal("expected dispatch 2 to succeed")
	}

	// Request 3 should be rejected because quota (2) is exhausted
	tr3, tw3 := pipe.New(pipe.WithoutSizeLimit())
	defer tw3.Close()
	if worker.Dispatch(ctx, &transport.Link{Reader: tr3, Writer: tw3}) {
		t.Fatal("expected dispatch 3 to fail due to exhausted request quota")
	}

	if !worker.IsRetired(time.Now()) {
		t.Fatal("expected worker to be retired")
	}
}

func TestClientWorkerRetirementSecs(t *testing.T) {
	r, w := pipe.New(pipe.WithoutSizeLimit())
	defer w.Close()

	worker, err := mux.NewClientWorker(transport.Link{Reader: r, Writer: w}, mux.ClientStrategy{
		MaxConcurrency:   4,
		HMaxReusableSecs: "1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer worker.Close()

	if worker.IsRetired(time.Now()) {
		t.Fatal("worker should not be retired immediately")
	}

	time.Sleep(1100 * time.Millisecond)

	if !worker.IsRetired(time.Now()) {
		t.Fatal("worker should be retired after reusable secs")
	}
}

func TestClientWorkerRetirementReuses(t *testing.T) {
	mockCtl := gomock.NewController(t)
	defer mockCtl.Finish()

	r1, w1 := pipe.New(pipe.WithoutSizeLimit())
	defer w1.Close()
	worker1, err := mux.NewClientWorker(transport.Link{Reader: r1, Writer: w1}, mux.ClientStrategy{
		MaxConcurrency: 4,
		CMaxReuseTimes: "1",
	})
	common.Must(err)
	defer worker1.Close()

	r2, w2 := pipe.New(pipe.WithoutSizeLimit())
	defer w2.Close()
	worker2, err := mux.NewClientWorker(transport.Link{Reader: r2, Writer: w2}, mux.ClientStrategy{
		MaxConcurrency: 4,
		CMaxReuseTimes: "1",
	})
	common.Must(err)
	defer worker2.Close()

	factory := mocks.NewMuxClientWorkerFactory(mockCtl)
	gomock.InOrder(
		factory.EXPECT().Create().Return(worker1, nil),
		factory.EXPECT().Create().Return(worker2, nil),
	)

	picker := &mux.IncrementalWorkerPicker{
		Factory: factory,
	}

	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})

	// 1st pick: worker1 is created (first use, not a reuse)
	w, err := picker.PickAvailable()
	if err != nil || w != worker1 {
		t.Fatalf("expected worker1, got %v, err %v", w, err)
	}
	if worker1.IsRetired(time.Now()) {
		t.Fatal("worker1 should not be retired on first use")
	}
	tr0, tw0 := pipe.New(pipe.WithoutSizeLimit())
	defer tw0.Close()
	if !worker1.Dispatch(ctx, &transport.Link{Reader: tr0, Writer: tw0}) {
		t.Fatal("expected dispatch on worker1 (first use) to succeed")
	}

	// 2nd pick: worker1 is reused (1 of 1 reuse quota)
	wReuse, err := picker.PickAvailable()
	if err != nil || wReuse != worker1 {
		t.Fatalf("expected worker1 on first reuse, got %v, err %v", wReuse, err)
	}
	// After exhausting reuse quota (leftReuseTimes == 0), worker1 must be retired for picker
	if !worker1.IsRetired(time.Now()) {
		t.Fatal("worker1 should be retired after exhausting reuse quota")
	}

	// Verify that the stream granted the final reuse can still successfully dispatch
	tr1, tw1 := pipe.New(pipe.WithoutSizeLimit())
	defer tw1.Close()
	if !worker1.Dispatch(ctx, &transport.Link{Reader: tr1, Writer: tw1}) {
		t.Fatal("expected dispatch on worker1 to succeed on its final reuse")
	}

	// 3rd pick: worker1 is retired, so picker must create worker2
	wNext, err := picker.PickAvailable()
	if err != nil || wNext != worker2 {
		t.Fatalf("expected worker2 after worker1 retired, got %v, err %v", wNext, err)
	}
}

func TestIncrementalPickerRetirement(t *testing.T) {
	mockCtl := gomock.NewController(t)
	defer mockCtl.Finish()

	r1, w1 := pipe.New(pipe.WithoutSizeLimit())
	defer w1.Close()
	worker1, err := mux.NewClientWorker(transport.Link{Reader: r1, Writer: w1}, mux.ClientStrategy{
		MaxConcurrency:   4,
		HMaxRequestTimes: "1",
	})
	common.Must(err)
	defer worker1.Close()

	r2, w2 := pipe.New(pipe.WithoutSizeLimit())
	defer w2.Close()
	worker2, err := mux.NewClientWorker(transport.Link{Reader: r2, Writer: w2}, mux.ClientStrategy{
		MaxConcurrency:   4,
		HMaxRequestTimes: "1",
	})
	common.Must(err)
	defer worker2.Close()

	factory := mocks.NewMuxClientWorkerFactory(mockCtl)
	gomock.InOrder(
		factory.EXPECT().Create().Return(worker1, nil),
		factory.EXPECT().Create().Return(worker2, nil),
	)

	picker := &mux.IncrementalWorkerPicker{
		Factory: factory,
	}

	// First pick: creates worker1
	w, err := picker.PickAvailable()
	if err != nil || w != worker1 {
		t.Fatalf("expected worker1, got %v, err %v", w, err)
	}

	// Dispatch request on worker1 (exhausts quota of 1)
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{
		Target: net.TCPDestination(net.DomainAddress("www.example.com"), 80),
	}})
	tr1, tw1 := pipe.New(pipe.WithoutSizeLimit())
	defer tw1.Close()
	if !worker1.Dispatch(ctx, &transport.Link{Reader: tr1, Writer: tw1}) {
		t.Fatal("expected dispatch on worker1 to succeed")
	}

	// Second pick: worker1 is retired, so picker creates worker2
	wNext, err := picker.PickAvailable()
	if err != nil || wNext != worker2 {
		t.Fatalf("expected worker2 after worker1 retired, got %v, err %v", wNext, err)
	}
}

