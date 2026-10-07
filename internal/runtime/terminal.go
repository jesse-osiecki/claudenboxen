package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/containerd/console"
	"golang.org/x/sys/unix"
)

// Host returns the host tty for callers that drive raw mode and sizing
// themselves rather than through Attach — the VM backend, where QEMU
// inherits stdio directly and there's no pty to relay.
func Host() (console.Console, error) {
	return hostConsole()
}

// hostConsole finds a real tty among stdin/stdout/stderr. Unlike
// console.Current() it returns an error rather than panicking when there
// is none.
func hostConsole() (console.Console, error) {
	var lastErr error
	for _, f := range []*os.File{os.Stderr, os.Stdout, os.Stdin} {
		if c, err := console.ConsoleFromFile(f); err == nil {
			return c, nil
		} else {
			lastErr = err
		}
	}
	return nil, fmt.Errorf("no controlling terminal on stdin/stdout/stderr (claudenboxen needs an interactive terminal): %w", lastErr)
}

// Attach puts the host terminal into raw mode, relays it against the
// sandbox's pty (sized to the host immediately and on every SIGWINCH), and
// blocks until the sandboxed process closes its end. Returns nil on a
// clean close.
func Attach(sandbox console.Console) error {
	host, err := hostConsole()
	if err != nil {
		return err
	}
	defer host.Reset()
	if err := host.SetRaw(); err != nil {
		return err
	}

	resize := func() {
		if size, err := host.Size(); err == nil {
			_ = sandbox.Resize(size)
		}
	}
	resize()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, unix.SIGWINCH)
	defer signal.Stop(winch)
	stopResize := make(chan struct{})
	go func() {
		for {
			select {
			case <-winch:
				resize()
			case <-stopResize:
				return
			}
		}
	}()
	defer close(stopResize)

	// ^C etc. reach the sandboxed foreground process group via the pty's
	// line discipline; no signal forwarding needed.
	go io.Copy(sandbox, host) //nolint:errcheck
	_, err = io.Copy(host, sandbox)
	// A pty master reads EIO, not EOF, once the slave side is fully
	// closed; that's the normal "process exited" signal here.
	if errors.Is(err, unix.EIO) {
		return nil
	}
	return err
}
