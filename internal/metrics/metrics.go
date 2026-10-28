// Package metrics holds the Prometheus plumbing shared by every mode: the CSI RPC
// interceptor and the HTTP endpoint (docs/reference/metrics.md).
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Namespace prefixes every series.
const Namespace = "csi_rclone"

// NewRegistry returns a registry with the Go runtime and process collectors.
func NewRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// GRPC counts and times CSI RPCs by method and gRPC code.
type GRPC struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewGRPC registers the RPC series on reg.
func NewGRPC(reg prometheus.Registerer) *GRPC {
	g := &GRPC{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "requests_total", Help: "CSI RPCs by method and gRPC code.",
		}, []string{"method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "request_duration_seconds", Help: "CSI RPC latency by method.",
			Buckets: []float64{.005, .01, .05, .1, .5, 1, 5, 10, 30, 60, 120},
		}, []string{"method"}),
	}
	reg.MustRegister(g.requests, g.duration)
	return g
}

// Interceptor records each unary call; now is injected for tests.
func (g *GRPC) Interceptor(now func() time.Time) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := now()
		resp, err := h(ctx, req)
		g.duration.WithLabelValues(info.FullMethod).Observe(now().Sub(start).Seconds())
		g.requests.WithLabelValues(info.FullMethod, status.Code(err).String()).Inc()
		return resp, err
	}
}

// Handler serves /metrics from g and, when healthz is set, /healthz (200 when it returns true).
func Handler(g prometheus.Gatherer, healthz func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{}))
	if healthz != nil {
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			if !healthz() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("ok"))
		})
	}
	return mux
}

// Serve listens on addr until ctx is done. An empty addr disables the endpoint.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	if addr == "" {
		return nil
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("metrics: listen: %w", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	log.Info("serving metrics", "addr", l.Addr().String())
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics: serve: %w", err)
	}
	return nil
}

// ServeBackground runs Serve in a goroutine and logs its failure: metrics never stop the driver.
func ServeBackground(ctx context.Context, addr string, h http.Handler, log *slog.Logger) {
	go func() {
		if err := Serve(ctx, addr, h, log); err != nil {
			log.Error("metrics endpoint", "err", err)
		}
	}()
}
