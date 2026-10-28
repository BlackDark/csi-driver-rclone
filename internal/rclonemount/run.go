package rclonemount

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BlackDark/csi-driver-rclone/internal/handoff"
	"github.com/BlackDark/csi-driver-rclone/internal/metrics"
	"github.com/BlackDark/csi-driver-rclone/internal/mountspec"
	_ "github.com/rclone/rclone/backend/all" // every backend a Remote may use
	_ "github.com/rclone/rclone/cmd/mount2"  // registers the mount2 MountFn
	"github.com/rclone/rclone/cmd/mountlib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configstruct"
	rclog "github.com/rclone/rclone/fs/log"
	"github.com/rclone/rclone/lib/atexit"
)

// Params configure Run. Durations of zero are invalid.
type Params struct {
	Spec             mountspec.Spec
	SockPath         string
	CacheDir         string
	DrainTimeout     time.Duration // bound on waiting for the upload queue after stop
	UnmountWait      time.Duration // how long to wait for the plugin's external unmount
	ReconnectBackoff time.Duration
	DrainPoll        time.Duration
	Log              *slog.Logger
	// MetricsAddr serves /metrics and /healthz; empty disables them. VolumeID labels the series.
	MetricsAddr string
	VolumeID    string
}

// Run serves the mount until ctx is cancelled (SIGTERM) or the mount goes away, then
// drains and reports the outcome to the node plugin. It returns nil on an orderly stop.
func Run(ctx context.Context, p Params) error {
	// rclone's atexit would os.Exit on SIGTERM before the drain; the caller owns signals.
	atexit.IgnoreSignals()
	routeRcloneLogs(p.Log)
	opts, err := prepare(p)
	if err != nil {
		return err
	}
	var ready atomic.Bool
	col := newCollector(p.VolumeID, globalTransfers)
	reg := metrics.NewRegistry()
	reg.MustRegister(col)
	mctx, stopMetrics := context.WithCancel(context.WithoutCancel(ctx))
	defer stopMetrics()
	metrics.ServeBackground(mctx, p.MetricsAddr, metrics.Handler(reg, ready.Load), p.Log)
	conn, err := handoff.Dial(ctx, p.SockPath)
	if err != nil {
		return err
	}
	fd, err := conn.ReceiveFD()
	if err != nil {
		_ = conn.Close()
		return err
	}
	p.Log.Info("received fuse fd", "fd", fd)
	mp, err := mount(p.Spec.Remote.FsPath, "/dev/fd/"+strconv.Itoa(fd), opts)
	if err != nil {
		_ = conn.Close()
		return err
	}
	col.setVFS(mp.VFS.Stats)
	session := handoff.NewSession(p.SockPath, conn, p.ReconnectBackoff, p.Log)
	defer func() { _ = session.Close() }()
	if err := session.Send(handoff.MsgReady); err != nil {
		return err
	}
	p.Log.Info("mount ready", "fs", p.Spec.Remote.FsPath, "identity", p.Spec.Identity)
	ready.Store(true)
	go session.Run(context.WithoutCancel(ctx))
	return stop(ctx, p, mp, session, func() { ready.Store(false) })
}

func prepare(p Params) (Options, error) {
	if err := LoadConfig(p.Spec.Remote.ConfigData, p.Spec.ConfigPassword); err != nil {
		return Options{}, err
	}
	opts, err := BuildOptions(p.Spec.Options)
	if err != nil {
		return Options{}, err
	}
	if err := config.SetCacheDir(p.CacheDir); err != nil {
		return Options{}, fmt.Errorf("rclonemount: cache dir: %w", err)
	}
	// One mount per process: the global config is this mount's config.
	ci := fs.GetConfig(context.Background())
	if err := configstruct.Set(opts.Global, ci); err != nil {
		return Options{}, fmt.Errorf("rclonemount: global options: %w", err)
	}
	if err := ci.Reload(context.Background()); err != nil {
		return Options{}, fmt.Errorf("rclonemount: reload config: %w", err)
	}
	rclog.Handler.SetLevel(fs.LogLevelToSlog(ci.LogLevel))
	return opts, nil
}

func mount(fsPath, mountpoint string, opts Options) (*mountlib.MountPoint, error) {
	_, mountFn := mountlib.ResolveMountMethod("mount2")
	if mountFn == nil {
		return nil, errors.New("rclonemount: mount2 is not available on this platform")
	}
	f, err := fs.NewFs(context.Background(), fsPath)
	if err != nil {
		return nil, fmt.Errorf("rclonemount: open remote: %w", err)
	}
	opts.Mount.DeviceName = fsPath
	mp := mountlib.NewMountPoint(mountFn, mountpoint, f, &opts.Mount, &opts.VFS)
	if _, err := mp.Mount(); err != nil {
		return nil, fmt.Errorf("rclonemount: %w", err)
	}
	return mp, nil
}

// stop waits for SIGTERM or the end of serving, drains, reports, and waits briefly
// for the plugin to unmount: rclone cannot unmount a /dev/fd/N mountpoint itself.
// notReady runs once stopping starts.
func stop(ctx context.Context, p Params, mp *mountlib.MountPoint, session *handoff.Session, notReady func()) error {
	var serveErr error
	served := false
	select {
	case <-ctx.Done():
		p.Log.Info("stop requested, draining", "timeout", p.DrainTimeout)
	case serveErr = <-mp.ErrChan:
		served = true
		p.Log.Warn("fuse server stopped before stop request", "err", serveErr)
	}
	notReady()
	msg := handoff.MsgDrained
	if !drain(context.Background(), mp.VFS.Stats, p.DrainTimeout, p.DrainPoll) {
		msg = handoff.MsgDrainTimeout
		p.Log.Warn("drain timed out, uploads may remain in the cache")
	}
	if err := session.Send(msg); err != nil {
		p.Log.Warn("report drain result", "msg", msg, "err", err)
	}
	p.Log.Info("drain finished", "result", msg)
	if !served {
		select {
		case serveErr = <-mp.ErrChan:
			p.Log.Info("unmounted externally")
		case <-time.After(p.UnmountWait):
			p.Log.Warn("no external unmount, exiting anyway", "waited", p.UnmountWait)
		}
	}
	mp.VFS.Shutdown()
	return serveErr
}

// routeRcloneLogs sends rclone's own log lines through log as structured records.
func routeRcloneLogs(log *slog.Logger) {
	rclog.Handler.SetOutput(func(level slog.Level, text string) {
		log.Log(context.Background(), level, strings.TrimSpace(text), "source", "rclone")
	})
}
