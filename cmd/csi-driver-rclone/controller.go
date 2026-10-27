package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/BlackDark/csi-driver-rclone/internal/controller"
	"github.com/BlackDark/csi-driver-rclone/internal/csiserver"
	"github.com/BlackDark/csi-driver-rclone/internal/identity"
)

type controllerFlags struct {
	endpoint string
	logLevel slog.Level
}

func parseControllerFlags(args []string, stderr io.Writer) (controllerFlags, error) {
	var f controllerFlags
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.endpoint, "endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	fs.TextVar(&f.logLevel, "log-level", slog.LevelInfo, "log level")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("controller: unexpected arguments %v", fs.Args())
	}
	return f, nil
}

func runController(args []string, stderr io.Writer) error {
	f, err := parseControllerFlags(args, stderr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: f.logLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return csiserver.Serve(ctx, f.endpoint, log, func(g *grpc.Server) {
		csi.RegisterIdentityServer(g, &identity.Server{Version: version, Controller: true})
		csi.RegisterControllerServer(g, &controller.Server{Log: log})
	})
}
