// Command csi-driver-rclone runs the rclone CSI driver in one of its modes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

var modes = []string{"node", "controller", "mount"}

var errNotImplemented = errors.New("not implemented")

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run returns the process exit code: 0 on success or help, 1 on mode failure, 2 on usage errors.
func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("csi-driver-rclone", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() == 0 {
		usage(stderr)
		return 2
	}
	err := dispatch(fs.Arg(0), fs.Args()[1:], stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUnknownMode):
		fmt.Fprintln(stderr, err)
		usage(stderr)
		return 2
	default:
		fmt.Fprintln(stderr, err)
		return 1
	}
}

var errUnknownMode = errors.New("unknown mode")

func dispatch(mode string, args []string, stderr io.Writer) error {
	known := false
	for _, m := range modes {
		known = known || m == mode
	}
	if !known {
		return fmt.Errorf("%w %q", errUnknownMode, mode)
	}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return fmt.Errorf("%s: %w", mode, errNotImplemented)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: csi-driver-rclone <mode> [flags]\n\nModes: %v\n", modes)
}
