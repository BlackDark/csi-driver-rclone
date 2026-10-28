package csiserver

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/BlackDark/csi-driver-rclone/internal/identity"
)

func TestSocketPath(t *testing.T) {
	for in, want := range map[string]string{"unix:///csi/csi.sock": "/csi/csi.sock", "unix:/a": "/a", "/b": "/b"} {
		if got, err := SocketPath(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	if _, err := SocketPath("tcp://x:1"); err == nil {
		t.Error("tcp accepted")
	}
}

type secretNode struct{ csi.UnimplementedNodeServer }

func (secretNode) NodePublishVolume(context.Context, *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	return &csi.NodePublishVolumeResponse{}, nil
}

func TestServeLogsWithoutSecrets(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "csi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, "unix://"+sock, log, func(s *grpc.Server) {
			csi.RegisterIdentityServer(s, &identity.Server{})
			csi.RegisterNodeServer(s, secretNode{})
		})
	}()
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// WaitForReady: Serve may not listen yet when the first call goes out.
	if _, err := csi.NewIdentityClient(conn).Probe(ctx, &csi.ProbeRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatal(err)
	}
	req := &csi.NodePublishVolumeRequest{VolumeId: "v", Secrets: map[string]string{"k": "TOPSECRET"}}
	if _, err := csi.NewNodeClient(conn).NodePublishVolume(ctx, req); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "TOPSECRET") || !strings.Contains(buf.String(), "NodePublishVolume") {
		t.Errorf("log: %s", buf.String())
	}
}
