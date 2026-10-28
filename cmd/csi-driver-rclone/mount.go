package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
	"github.com/BlackDark/csi-driver-rclone/internal/rclonemount"
)

// mountFlags are the Mount Pod's settings; defaults match docs/design/mount-pod-contract.md.
type mountFlags struct {
	spec, password, socket, cacheDir string
	metricsAddr, volumeID            string
	drainTimeout, unmountWait        time.Duration
}

func parseMountFlags(args []string, stderr io.Writer) (mountFlags, error) {
	var f mountFlags
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.spec, "spec", "/etc/rclone-mount/spec.json", "Mount Spec file")
	fs.StringVar(&f.password, "config-password", "/etc/rclone-mount/config-password", "config password file; missing means none")
	fs.StringVar(&f.socket, "socket", "/run/rclone-mount/handoff.sock", "node plugin handoff socket")
	fs.StringVar(&f.cacheDir, "cache-dir", "/cache", "rclone VFS cache directory")
	fs.StringVar(&f.metricsAddr, "metrics-addr", ":9810", "address for /metrics and /healthz; empty disables")
	fs.StringVar(&f.volumeID, "volume-id", os.Getenv("VOLUME_ID"), "volume ID labelling the metrics (env VOLUME_ID)")
	fs.DurationVar(&f.drainTimeout, "drain-timeout", 5*time.Minute, "bound on waiting for pending uploads on stop")
	fs.DurationVar(&f.unmountWait, "unmount-wait", 30*time.Second, "how long to wait for the node plugin to unmount after draining")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("mount: unexpected arguments %v", fs.Args())
	}
	return f, nil
}

// readPassword returns "" when the optional file is absent.
func readPassword(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("mount: read config password: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func loadSpec(f mountFlags) (mountspec.Spec, error) {
	data, err := os.ReadFile(f.spec)
	if err != nil {
		return mountspec.Spec{}, fmt.Errorf("mount: read spec: %w", err)
	}
	password, err := readPassword(f.password)
	if err != nil {
		return mountspec.Spec{}, err
	}
	return mountspec.Load(data, password)
}

func runMount(args []string, stderr io.Writer) error {
	f, err := parseMountFlags(args, stderr)
	if err != nil {
		return err
	}
	spec, err := loadSpec(f)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return rclonemount.Run(ctx, rclonemount.Params{
		Spec:             spec,
		SockPath:         f.socket,
		CacheDir:         f.cacheDir,
		DrainTimeout:     f.drainTimeout,
		UnmountWait:      f.unmountWait,
		ReconnectBackoff: time.Second,
		DrainPoll:        time.Second,
		MetricsAddr:      f.metricsAddr,
		VolumeID:         f.volumeID,
		Log:              slog.New(slog.NewJSONHandler(os.Stdout, nil)),
	})
}
