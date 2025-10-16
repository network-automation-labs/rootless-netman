package netman

import (
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"syscall"

	"github.com/coreos/go-systemd/v22/activation"
	"go.podman.io/common/libnetwork/types"
)

type Server struct {
	Netman
}

func NewServer() (*Server, error) {
	netman, err := NewNetman()
	return &Server{
		Netman: netman,
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
	server := rpc.NewServer()
	err := server.RegisterName("Netman", s)
	if err == nil {
		server.Accept(listener)
	}
	return err
}

func (s *Server) Connect(options SetupNetworkOptions, statusBlock *types.StatusBlock) (err error) {
	*statusBlock, err = s.Netman.Connect(&options)
	return err
}

func (s *Server) Disconnect(options TeardownNetworkOptions, _ *struct{}) error {
	return s.Netman.Disconnect(&options)
}

func (s *Server) Inspect(name string, network *types.Network) (err error) {
	*network, err = s.Netman.Inspect(name)
	return err
}
