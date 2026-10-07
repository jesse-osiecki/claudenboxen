package runtime

import (
	"bytes"
	"fmt"
	"net"
	"os"

	"github.com/containerd/console"
	"golang.org/x/sys/unix"
)

// ConsoleSocket listens on a fresh AF_UNIX socket at path and hands back the
// pty master fd that runsc sends over it once the sandbox starts (the same
// --console-socket convention runc/crun use).
type ConsoleSocket struct {
	Path     string
	listener *net.UnixListener
}

// ListenConsoleSocket creates the socket ready for runsc to connect to. The
// caller must call Accept after starting `runsc run` and Close when done.
func ListenConsoleSocket(path string) (*ConsoleSocket, error) {
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("resolve console socket addr: %w", err)
	}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen console socket: %w", err)
	}
	return &ConsoleSocket{Path: path, listener: l}, nil
}

// Accept blocks for runsc's single connection carrying the pty master fd and
// returns it wrapped as a console.Console.
func (c *ConsoleSocket) Accept() (console.Console, error) {
	conn, err := c.listener.Accept()
	if err != nil {
		return nil, fmt.Errorf("accept console socket: %w", err)
	}
	defer conn.Close()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("console socket connection is not unix")
	}
	f, err := recvFd(unixConn)
	if err != nil {
		return nil, fmt.Errorf("receive pty master fd: %w", err)
	}
	return console.ConsoleFromFile(f)
}

// Close removes the listener and the socket file.
func (c *ConsoleSocket) Close() error {
	err := c.listener.Close()
	os.Remove(c.Path)
	return err
}

// recvFd reads a single file descriptor sent via SCM_RIGHTS, the same
// mechanism runc's libcontainer/utils.RecvFd uses.
func recvFd(conn *net.UnixConn) (*os.File, error) {
	const maxNameLen = 4096
	oobSpace := unix.CmsgSpace(4)

	name := make([]byte, maxNameLen)
	oob := make([]byte, oobSpace)

	n, oobn, _, _, err := conn.ReadMsgUnix(name, oob)
	if err != nil {
		return nil, err
	}
	if n >= maxNameLen || oobn != oobSpace {
		return nil, fmt.Errorf("recvfd: unexpected message size (n=%d oobn=%d)", n, oobn)
	}

	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, err
	}
	if len(scms) != 1 {
		return nil, fmt.Errorf("recvfd: expected 1 control message, got %d", len(scms))
	}

	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil {
		return nil, err
	}
	if len(fds) != 1 {
		return nil, fmt.Errorf("recvfd: expected 1 fd, got %d", len(fds))
	}

	fdName := string(bytes.TrimRight(name[:n], "\x00"))
	return os.NewFile(uintptr(fds[0]), fdName), nil
}
