package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInterceptorCountsByMethodAndCode(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := NewGRPC(reg)
	clock := time.Unix(0, 0)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }
	ic := g.Interceptor(now)
	info := &grpc.UnaryServerInfo{FullMethod: "/csi.v1.Node/NodePublishVolume"}
	ok := func(context.Context, any) (any, error) { return "r", nil }
	bad := func(context.Context, any) (any, error) { return nil, status.Error(codes.InvalidArgument, "x") }
	for _, h := range []grpc.UnaryHandler{ok, ok, bad} {
		_, _ = ic(context.Background(), nil, info, h)
	}
	want := `
# HELP csi_rclone_requests_total CSI RPCs by method and gRPC code.
# TYPE csi_rclone_requests_total counter
csi_rclone_requests_total{code="InvalidArgument",method="/csi.v1.Node/NodePublishVolume"} 1
csi_rclone_requests_total{code="OK",method="/csi.v1.Node/NodePublishVolume"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "csi_rclone_requests_total"); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(g.duration); n != 1 {
		t.Fatalf("histogram series = %d, want 1", n)
	}
	sum := testutil.ToFloat64(prometheus.Collector(g.requests.WithLabelValues("/csi.v1.Node/NodePublishVolume", "OK")))
	if sum != 2 {
		t.Fatalf("ok count = %v", sum)
	}
}

func TestHandlerHealthz(t *testing.T) {
	var ready atomic.Bool
	srv := httptest.NewServer(Handler(prometheus.NewRegistry(), ready.Load))
	defer srv.Close()
	get := func(path string) int {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		return r.StatusCode
	}
	if c := get("/healthz"); c != http.StatusServiceUnavailable {
		t.Fatalf("not ready = %d", c)
	}
	ready.Store(true)
	if c := get("/healthz"); c != http.StatusOK {
		t.Fatalf("ready = %d", c)
	}
	if c := get("/metrics"); c != http.StatusOK {
		t.Fatalf("metrics = %d", c)
	}
}

func TestHandlerWithoutHealthz(t *testing.T) {
	srv := httptest.NewServer(Handler(prometheus.NewRegistry(), nil))
	defer srv.Close()
	r, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("healthz without check = %d", r.StatusCode)
	}
}
