// Package csiserver runs a gRPC server for CSI services on a unix socket.
package csiserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"google.golang.org/grpc"
)

// SocketPath parses "unix:///path" or "unix:/path" or a bare path.
func SocketPath(endpoint string) (string, error) {
	p := endpoint
	switch {
	case strings.HasPrefix(p, "unix://"):
		p = strings.TrimPrefix(p, "unix://")
	case strings.HasPrefix(p, "unix:"):
		p = strings.TrimPrefix(p, "unix:")
	case strings.Contains(p, "://"):
		return "", fmt.Errorf("csiserver: unsupported endpoint %q, want unix://", endpoint)
	}
	if p == "" {
		return "", fmt.Errorf("csiserver: empty endpoint")
	}
	return p, nil
}

// Serve listens on endpoint until ctx is done. register adds the CSI services; extra
// interceptors run inside the logging one.
func Serve(ctx context.Context, endpoint string, log *slog.Logger, register func(*grpc.Server), extra ...grpc.UnaryServerInterceptor) error {
	path, err := SocketPath(endpoint)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("csiserver: remove stale socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("csiserver: listen: %w", err)
	}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(append([]grpc.UnaryServerInterceptor{LogInterceptor(log)}, extra...)...))
	register(srv)
	stop := context.AfterFunc(ctx, srv.GracefulStop)
	defer stop()
	log.Info("serving CSI", "endpoint", path)
	if err := srv.Serve(l); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("csiserver: serve: %w", err)
	}
	return nil
}

// LogInterceptor logs every call with secrets stripped from requests and replies.
func LogInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		log.Debug("csi call", "method", info.FullMethod, "request", protosanitizer.StripSecrets(req))
		resp, err := h(ctx, req)
		if err != nil {
			log.Error("csi call failed", "method", info.FullMethod, "err", err)
		} else {
			log.Debug("csi reply", "method", info.FullMethod, "response", protosanitizer.StripSecrets(resp))
		}
		return resp, err
	}
}
