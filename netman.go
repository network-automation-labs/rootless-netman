package netman

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.podman.io/common/libnetwork/network"
	"go.podman.io/common/libnetwork/types"
	"go.podman.io/common/pkg/config"

	"go.podman.io/storage"
)

type Netman interface {
	Inspect(name string) (types.Network, error)
	Connect(options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error)
	Disconnect(options *TeardownNetworkOptions) error
}

type Backend interface {
	Inspect(name string) (types.Network, error)
	Connect(clientPid int, options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error)
	Disconnect(clientPid int, options *TeardownNetworkOptions) error
}

type netmanBackend struct {
	containerNetwork types.ContainerNetwork
	ns               nsOps

	cleanupInterval time.Duration

	// mu serializes every Setup/Teardown call plus the state bookkeeping
	// around it, across Connect, Disconnect, and the cleanup loop. The
	// vendored network backend's own concurrency safety is unverified, and
	// this is the daemon's single point of truth for which tenants are
	// currently connected.
	mu    sync.Mutex
	state StateStore
}

type Option func(*netmanBackend) error

func CleanupInterval(interval time.Duration) Option {
	return func(backend *netmanBackend) error {
		if interval <= 0 {
			return fmt.Errorf("Invalid cleanup interval, it must be greater than zero")
		}
		backend.cleanupInterval = interval
		return nil
	}
}

func NewNetman(options ...Option) (*netmanBackend, error) {
	storageOptions, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, err
	}

	store, err := storage.GetStore(storageOptions)
	if err != nil {
		return nil, err
	}

	conf, err := config.Default()
	if err != nil {
		return nil, err
	}

	_, netInterface, err := network.NetworkBackend(store, conf, false)
	if err != nil {
		return nil, err
	}

	state, err := loadStateStore(NetmanStatePath)
	if err != nil {
		return nil, err
	}

	backend := &netmanBackend{containerNetwork: netInterface, ns: kernelNSOps{}, state: state}
	for _, option := range options {
		err := option(backend)
		if err != nil {
			return nil, err
		}
	}

	go backend.cleanupLoop()
	return backend, nil
}

func getSetupOptions(options *SetupNetworkOptions) types.NetworkOptions {
	return types.NetworkOptions{
		ContainerID:   options.ContainerID,
		ContainerName: options.ContainerName,
		Networks: map[string]types.PerNetworkOptions{
			options.Network.Name: options.NetworkOptions,
		},
	}
}

func (n *netmanBackend) Inspect(name string) (types.Network, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.containerNetwork.NetworkInspect(name)
}

func (n *netmanBackend) Disconnect(clientPid int, options *TeardownNetworkOptions) error {
	// Verify, via the kernel-verified clientPid, that the caller is
	// currently in the namespace it claims (options.ContainerNS) before
	// touching any tracked state. ContainerID/Network.Name alone are
	// client-supplied and are not sufficient authorization by themselves —
	// without this check, any authenticated caller could name another
	// tenant's ContainerID/Network and tear down its connection.
	verifiedPath, err := n.ns.ResolvePath(clientPid, options.ContainerNS)
	if err != nil {
		logrus.Errorf("Failed disconnect container %v", err)
		return err
	}

	networkOptions := getSetupOptions(&options.SetupNetworkOptions)
	logrus.Debugf("Disconnecting %s from %s: %+v", options.ContainerName, options.Network.Name, networkOptions)

	n.mu.Lock()
	defer n.mu.Unlock()

	// Prefer the tracked connection's pinned NSPath over the freshly
	// resolved /proc/<pid>/ns/net: it stays valid regardless of the
	// current caller's pid, so Teardown/unmount always target the
	// namespace this connection actually pinned. Only trust the tracked
	// record when its own recorded inode matches the inode the caller just
	// proved ownership of — otherwise ContainerID/Network.Name could be
	// used to reach another tenant's pinned path.
	nspath := verifiedPath
	conn, tracked := n.state.Get(options.ContainerID, options.Network.Name)
	if tracked && conn.NSInode != options.ContainerNS {
		logrus.Warnf("Disconnect for %s/%s does not match the verified caller's namespace; ignoring tracked state", options.ContainerID, options.Network.Name)
		tracked = false
	}
	if tracked {
		nspath = conn.NSPath
	}

	err = n.containerNetwork.Teardown(nspath, types.TeardownOptions{NetworkOptions: networkOptions})
	if err != nil {
		logrus.Errorf("Failed disconnect container %v", err)
		return err
	}

	if tracked {
		if unmountErr := n.ns.Unmount(conn.NSPath); unmountErr != nil {
			logrus.Errorf("Failed to unmount pinned namespace %s for %s: %v", conn.NSPath, options.ContainerName, unmountErr)
		}
		n.state.Delete(conn)
	}
	return nil
}

func (n *netmanBackend) Connect(clientPid int, options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error) {
	verifiedPath, err := n.ns.ResolvePath(clientPid, options.ContainerNS)
	if err != nil {
		logrus.Errorf("Failed to connect container %v", err)
		return statusBlock, err
	}

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.Debugf("Connecting %s to %s", options.ContainerName, options.Network.Name)
		logrus.Debugf("Connect options received: %+v", options)
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	// Pin the verified /proc/<pid>/ns/net reference to a durable,
	// bind-mounted file: the RPC caller exits almost immediately
	// after this call returns, so its pid may be recycled by the kernel
	// long before the cleanup process ever needs to check on, or tear
	// down, this connection again. Once pinned, NSPath stays valid
	// regardless of that pid's fate.
	nspath, err := n.ns.Mount(verifiedPath)
	if err != nil {
		logrus.Errorf("Failed to pin namespace for %s: %v", options.ContainerName, err)
		return statusBlock, err
	}
	tracked := false
	defer func() {
		if !tracked {
			if unmountErr := n.ns.Unmount(nspath); unmountErr != nil {
				logrus.Errorf("Failed to unmount pinned namespace %s for %s: %v", nspath, options.ContainerName, unmountErr)
			}
		}
	}()

	networkOptions := getSetupOptions(options)
	var statusBlocks map[string]types.StatusBlock
	statusBlocks, err = n.containerNetwork.Setup(nspath, types.SetupOptions{NetworkOptions: networkOptions})
	statusBlock = statusBlocks[options.Network.Name]

	if err == nil {
		interfaces := slices.Collect(maps.Keys(statusBlock.Interfaces))
		links := ContainerLinks{
			Options:    *options,
			NSPath:     nspath,
			Interfaces: interfaces,
		}
		err = n.ns.Isolate(links)
		if err != nil {
			logrus.Errorf("Failed to isolate %s, tearing down: %v", options.ContainerName, err)
			if teardownErr := n.containerNetwork.Teardown(nspath, types.TeardownOptions{NetworkOptions: networkOptions}); teardownErr != nil {
				logrus.Errorf("Failed to tear down %s after isolation failure: %v", options.ContainerName, teardownErr)
			}
		} else if links.NSInode, err = n.ns.Inode(nspath); err != nil {
			logrus.Errorf("Failed to record namespace inode for %s: %v", options.ContainerName, err)
			if teardownErr := n.containerNetwork.Teardown(nspath, types.TeardownOptions{NetworkOptions: networkOptions}); teardownErr != nil {
				logrus.Errorf("Failed to tear down %s after inode failure: %v", options.ContainerName, teardownErr)
			}
		} else {
			n.state.Put(links)
			tracked = true
		}
	}
	if err != nil {
		logrus.Errorf("Failed to connect container %v", err)
		statusBlock = types.StatusBlock{}
	}
	return statusBlock, err
}

// cleanupLinks tears down an orphaned connection and drops its record.
// Teardown is always attempted, even when the namespace looks fully gone:
// netavark's own IP allocations live in an IPAM db keyed independent of
// namespace existence, and skipping the call would leak that reservation
// even though the interface itself is already gone. The record is dropped
// unconditionally afterward — there is nothing left to retry against a
// namespace that no longer exists.
func (n *netmanBackend) cleanupLinks(conn ContainerLinks) {
	logrus.Warnf("Cleaning up orphaned connection %s", conn)

	n.mu.Lock()
	defer n.mu.Unlock()

	networkOptions := getSetupOptions(&conn.Options)
	if err := n.containerNetwork.Teardown(conn.NSPath, types.TeardownOptions{NetworkOptions: networkOptions}); err != nil {
		logrus.Errorf("Best-effort teardown failed for orphaned connection %s: %v", conn, err)
	}
	if err := n.ns.Unmount(conn.NSPath); err != nil {
		logrus.Errorf("Failed to unmount pinned namespace %s for orphaned connection %s: %v", conn.NSPath, conn, err)
	}
	n.state.Delete(conn)
}

// cleanup cleans up every tracked connection whose namespace or
// interfaces no longer exist.
func (n *netmanBackend) cleanup() {
	for _, conn := range n.state.GetLinks() {
		if !n.ns.Exists(conn) {
			n.cleanupLinks(conn)
		}
	}
}

// cleanupLoop runs reconcileOnce on a timer for the lifetime of the
// process. There is no stop channel: this matches the rest of the daemon's
// lifecycle (a long-running systemd service with no graceful-shutdown path
// today).
func (n *netmanBackend) cleanupLoop() {
	ticker := time.NewTicker(n.cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		n.cleanup()
	}
}
