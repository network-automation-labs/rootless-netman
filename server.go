package netman

import (
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"syscall"

	"github.com/coreos/go-systemd/v22/activation"
	"github.com/sirupsen/logrus"
	"go.podman.io/common/libnetwork/types"
)

type Server struct {
	backend Backend
}

func NewServer() (*Server, error) {
	netman, err := NewNetman()
	return &Server{
		backend: netman,
	}, err
}

func (s *Server) ServeSystemd() error {
	listeners, err := activation.Listeners()
	if err == nil && len(listeners) > 0 {
		return s.Serve(listeners[0])
	}
	return err
}

func (s *Server) ServeUnix(socketPath string) error {
	owner := uint32(0)

	socketDir := filepath.Dir(socketPath)
	fileInfo, err := os.Stat(socketDir)
	if err == nil {
		sysStat := fileInfo.Sys().(*syscall.Stat_t)
		owner = sysStat.Gid
	}
	listener, err := net.Listen("unix", socketPath)
	if err == nil {
		err = os.Chmod(socketPath, 0770)

		if err == nil {
			err = os.Chown(socketPath, -1, int(owner))
		}
	}
	return s.Serve(listener)
}

func (s *Server) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		go func(conn net.Conn) {
			defer conn.Close()

			cred, err := PeerCredentials(conn)
			if err != nil {
				logrus.Errorf("Rejecting connection: failed to verify peer credentials: %v", err)
				return
			}

			handler := &NetmanReceiver{
				backend: s.backend,
				peerPid: int(cred.Pid),
			}

			rpcServer := rpc.NewServer()
			if err := rpcServer.RegisterName("Netman", handler); err != nil {
				logrus.Errorf("Failed to register RPC handler: %v", err)
				return
			}

			logrus.Debugf("Accepted connection from uid=%d pid=%d", cred.Uid, cred.Pid)
			rpcServer.ServeConn(conn)
		}(conn)
	}
}

// NetmanReceiver serves the Netman RPCs for a single connection,
// carrying the kernel-verified identity (via SO_PEERCRED) of the process on
// the other end. peerPid, not any client-supplied PID, is what gets used to
// resolve which network namespace a Connect/Disconnect call may touch.
type NetmanReceiver struct {
	backend Backend
	peerPid int
}

func (h *NetmanReceiver) Connect(options SetupNetworkOptions, statusBlock *types.StatusBlock) (err error) {
	*statusBlock, err = h.backend.Connect(h.peerPid, &options)
	return err
}

func (h *NetmanReceiver) Disconnect(options TeardownNetworkOptions, _ *struct{}) error {
	return h.backend.Disconnect(h.peerPid, &options)
}

func (h *NetmanReceiver) Inspect(name string, network *types.Network) (err error) {
	*network, err = h.backend.Inspect(name)
	return err
}
