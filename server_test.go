package netman

import (
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"testing"

	"go.podman.io/common/libnetwork/types"
)

type fakeBackend struct {
	connectPid        int
	connectOptions    *SetupNetworkOptions
	disconnectPid     int
	disconnectOptions *TeardownNetworkOptions
}

func (f *fakeBackend) Inspect(name string) (types.Network, error) {
	return types.Network{Name: name}, nil
}

func (f *fakeBackend) Connect(clientPid int, options *SetupNetworkOptions) (types.StatusBlock, error) {
	f.connectPid = clientPid
	f.connectOptions = options
	return types.StatusBlock{}, nil
}

func (f *fakeBackend) Disconnect(clientPid int, options *TeardownNetworkOptions) error {
	f.disconnectPid = clientPid
	f.disconnectOptions = options
	return nil
}

func newTestServer(t *testing.T, backend Backend) string {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "server.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to listen on %s: %v", socketPath, err)
	}

	server := &Server{backend: backend}
	go server.Serve(listener)
	t.Cleanup(func() { listener.Close() })

	return socketPath
}

// TestServerConnectUsesVerifiedPeerPid verifies the fix for the root daemon
// authorization bypass: the daemon must use the SO_PEERCRED-verified pid of
// the actual connection when resolving which namespace to touch, never a
// value taken from the client. SetupNetworkOptions no longer even carries a
// client-supplied pid field, so this confirms the backend receives the real
// connecting process's pid by construction.
func TestServerConnectUsesVerifiedPeerPid(t *testing.T) {
	fake := &fakeBackend{}
	socketPath := newTestServer(t, fake)

	client, err := rpc.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to dial %s: %v", socketPath, err)
	}
	defer client.Close()

	statusBlock := types.StatusBlock{}
	if err := client.Call("Netman.Connect", SetupNetworkOptions{}, &statusBlock); err != nil {
		t.Fatalf("Netman.Connect call failed: %v", err)
	}

	if fake.connectOptions == nil {
		t.Fatal("fake backend Connect was not called")
	}
	if fake.connectPid != os.Getpid() {
		t.Errorf("Connect clientPid = %d; want verified peer pid %d", fake.connectPid, os.Getpid())
	}
}

func TestServerDisconnectUsesVerifiedPeerPid(t *testing.T) {
	fake := &fakeBackend{}
	socketPath := newTestServer(t, fake)

	client, err := rpc.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to dial %s: %v", socketPath, err)
	}
	defer client.Close()

	if err := client.Call("Netman.Disconnect", TeardownNetworkOptions{}, nil); err != nil {
		t.Fatalf("Netman.Disconnect call failed: %v", err)
	}

	if fake.disconnectOptions == nil {
		t.Fatal("fake backend Disconnect was not called")
	}
	if fake.disconnectPid != os.Getpid() {
		t.Errorf("Disconnect clientPid = %d; want verified peer pid %d", fake.disconnectPid, os.Getpid())
	}
}
