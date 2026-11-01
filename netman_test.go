package netman

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.podman.io/common/libnetwork/types"
)

// fakeContainerNetwork records Setup/Teardown calls. Methods the backend
// never calls are left to the embedded nil interface and panic if reached.
type fakeContainerNetwork struct {
	types.ContainerNetwork

	setupErr     error
	setupStatus  map[string]types.StatusBlock
	setupPaths   []string
	teardownErr  error
	teardownOpts []types.TeardownOptions
	teardowns    []string
}

func (f *fakeContainerNetwork) NetworkInspect(name string) (types.Network, error) {
	return types.Network{Name: name, Driver: "fake"}, nil
}

func (f *fakeContainerNetwork) Setup(path string, options types.SetupOptions) (map[string]types.StatusBlock, error) {
	f.setupPaths = append(f.setupPaths, path)
	return f.setupStatus, f.setupErr
}

func (f *fakeContainerNetwork) Teardown(path string, options types.TeardownOptions) error {
	f.teardowns = append(f.teardowns, path)
	f.teardownOpts = append(f.teardownOpts, options)
	return f.teardownErr
}

// fakeNSOps models a host where pid clientPid is in the namespace with inode
// clientInode at clientPath, and Pin always produces pinnedPath.
type fakeNSOps struct {
	clientPid   int
	clientInode uint64
	clientPath  string
	pinnedPath  string

	pinErr     error
	inode      uint64
	inodeErr   error
	isolateErr error
	missing    map[string]bool // keyed by NSPath

	pinned   []string
	unpinned []string
	isolated []ContainerLinks
}

func (f *fakeNSOps) ResolvePath(clientPid int, inode uint64) (string, error) {
	if clientPid != f.clientPid || inode != f.clientInode {
		return "", errors.New("no matching namespace")
	}
	return f.clientPath, nil
}

func (f *fakeNSOps) Mount(path string) (string, error) {
	if f.pinErr != nil {
		return "", f.pinErr
	}
	f.pinned = append(f.pinned, path)
	return f.pinnedPath, nil
}

func (f *fakeNSOps) Unmount(path string) error {
	f.unpinned = append(f.unpinned, path)
	return nil
}

func (f *fakeNSOps) Inode(path string) (uint64, error) {
	return f.inode, f.inodeErr
}

func (f *fakeNSOps) Isolate(links ContainerLinks) error {
	f.isolated = append(f.isolated, links)
	return f.isolateErr
}

func (f *fakeNSOps) Exists(links ContainerLinks) bool {
	return !f.missing[links.NSPath]
}

const (
	testPid        = 4242
	testInode      = uint64(1001)
	testClientPath = "/proc/4242/ns/net"
	testPinnedPath = "/run/netns/netman-pinned"
	testNetwork    = "external"
)

type testBackend struct {
	*netmanBackend
	network *fakeContainerNetwork
	ns      *fakeNSOps
}

func newTestBackend(t *testing.T) testBackend {
	t.Helper()

	state, err := loadStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("loadStateStore() error = %v", err)
	}

	network := &fakeContainerNetwork{
		setupStatus: map[string]types.StatusBlock{
			testNetwork: {Interfaces: map[string]types.NetInterface{"eth1": {}}},
		},
	}
	ns := &fakeNSOps{
		clientPid:   testPid,
		clientInode: testInode,
		clientPath:  testClientPath,
		pinnedPath:  testPinnedPath,
		inode:       testInode,
	}
	return testBackend{
		netmanBackend: &netmanBackend{containerNetwork: network, ns: ns, state: state},
		network:       network,
		ns:            ns,
	}
}

func testSetupOptions(containerID string) *SetupNetworkOptions {
	return &SetupNetworkOptions{
		ContainerID:   containerID,
		ContainerName: containerID + "-name",
		ContainerNS:   testInode,
		Network:       types.Network{Name: testNetwork},
	}
}

func testTeardownOptions(containerID string) *TeardownNetworkOptions {
	return &TeardownNetworkOptions{SetupNetworkOptions: *testSetupOptions(containerID)}
}

// trackedLinks returns a record as Connect would have stored it.
func trackedLinks(containerID string, nsPath string, inode uint64) ContainerLinks {
	return ContainerLinks{
		Options:    *testSetupOptions(containerID),
		NSPath:     nsPath,
		NSInode:    inode,
		Interfaces: []string{"eth1"},
	}
}

func assertTracked(t *testing.T, b testBackend, containerID string, want bool) {
	t.Helper()
	_, got := b.state.Get(containerID, testNetwork)
	if got != want {
		t.Errorf("state tracked %s = %v; want %v", containerID, got, want)
	}
}

func TestInspect(t *testing.T) {
	b := newTestBackend(t)
	network, err := b.Inspect(testNetwork)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if network.Name != testNetwork {
		t.Errorf("Inspect() name = %q; want %q", network.Name, testNetwork)
	}
}

func TestConnectTracksPinnedNamespace(t *testing.T) {
	b := newTestBackend(t)

	statusBlock, err := b.Connect(testPid, testSetupOptions("c1"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, ok := statusBlock.Interfaces["eth1"]; !ok {
		t.Errorf("Connect() status block = %+v; want interface eth1", statusBlock)
	}

	if !slices.Equal(b.ns.pinned, []string{testClientPath}) {
		t.Errorf("pinned = %v; want the verified client path", b.ns.pinned)
	}
	// Everything after pinning must use the durable path, never the
	// ephemeral /proc/<pid>/ns/net one.
	if !slices.Equal(b.network.setupPaths, []string{testPinnedPath}) {
		t.Errorf("Setup paths = %v; want pinned path", b.network.setupPaths)
	}
	if len(b.ns.isolated) != 1 || b.ns.isolated[0].NSPath != testPinnedPath ||
		!slices.Equal(b.ns.isolated[0].Interfaces, []string{"eth1"}) {
		t.Errorf("isolated = %+v; want eth1 at pinned path", b.ns.isolated)
	}
	if len(b.ns.unpinned) != 0 {
		t.Errorf("unpinned = %v; want none on success", b.ns.unpinned)
	}

	conn, tracked := b.state.Get("c1", testNetwork)
	if !tracked {
		t.Fatal("Connect() did not track the connection")
	}
	if conn.NSPath != testPinnedPath || conn.NSInode != testInode {
		t.Errorf("tracked = %+v; want NSPath %s, NSInode %d", conn, testPinnedPath, testInode)
	}
}

func TestConnectRejectsUnverifiedNamespace(t *testing.T) {
	b := newTestBackend(t)

	options := testSetupOptions("c1")
	options.ContainerNS = testInode + 1 // a namespace the caller is not in

	if _, err := b.Connect(testPid, options); err == nil {
		t.Fatal("Connect() error = nil; want namespace verification failure")
	}
	if len(b.ns.pinned) != 0 || len(b.network.setupPaths) != 0 {
		t.Errorf("pinned = %v, setup = %v; want nothing touched", b.ns.pinned, b.network.setupPaths)
	}
	assertTracked(t, b, "c1", false)
}

func TestConnectPinFailure(t *testing.T) {
	b := newTestBackend(t)
	b.ns.pinErr = errors.New("pin failed")

	if _, err := b.Connect(testPid, testSetupOptions("c1")); err == nil {
		t.Fatal("Connect() error = nil; want pin failure")
	}
	if len(b.network.setupPaths) != 0 {
		t.Errorf("Setup called with %v; want not called", b.network.setupPaths)
	}
	if len(b.ns.unpinned) != 0 {
		t.Errorf("unpinned = %v; want nothing to release", b.ns.unpinned)
	}
	assertTracked(t, b, "c1", false)
}

// TestConnectFailureRollsBack covers every failure after the namespace is
// pinned: each must release the pin, leave nothing tracked, and return an
// empty status block, and failures after a successful Setup must also tear
// it down. The fake Setup returns a populated status block even when it
// fails, so the setup case also checks a partial result isn't passed through.
func TestConnectFailureRollsBack(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(testBackend)
		wantTeardown bool
	}{
		{
			name:      "setup",
			configure: func(b testBackend) { b.network.setupErr = errors.New("setup failed") },
		},
		{
			name:         "isolate",
			configure:    func(b testBackend) { b.ns.isolateErr = errors.New("isolate failed") },
			wantTeardown: true,
		},
		{
			name:         "inode",
			configure:    func(b testBackend) { b.ns.inodeErr = errors.New("stat failed") },
			wantTeardown: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newTestBackend(t)
			test.configure(b)

			statusBlock, err := b.Connect(testPid, testSetupOptions("c1"))
			if err == nil {
				t.Fatal("Connect() error = nil; want failure")
			}
			if len(statusBlock.Interfaces) != 0 {
				t.Errorf("Connect() status block = %+v; want empty on failure", statusBlock)
			}

			if !slices.Equal(b.ns.unpinned, []string{testPinnedPath}) {
				t.Errorf("unpinned = %v; want pinned path released", b.ns.unpinned)
			}
			var wantTeardowns []string
			if test.wantTeardown {
				wantTeardowns = []string{testPinnedPath}
			}
			if !slices.Equal(b.network.teardowns, wantTeardowns) {
				t.Errorf("teardowns = %v; want %v", b.network.teardowns, wantTeardowns)
			}
			assertTracked(t, b, "c1", false)
		})
	}
}

func TestDisconnectTrackedUsesPinnedPath(t *testing.T) {
	b := newTestBackend(t)
	b.state.Put(trackedLinks("c1", testPinnedPath, testInode))

	if err := b.Disconnect(testPid, testTeardownOptions("c1")); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if !slices.Equal(b.network.teardowns, []string{testPinnedPath}) {
		t.Errorf("teardowns = %v; want pinned path", b.network.teardowns)
	}
	if got := b.network.teardownOpts[0].NetworkOptions.Networks; len(got) != 1 {
		t.Errorf("teardown networks = %v; want only %s", got, testNetwork)
	} else if _, ok := got[testNetwork]; !ok {
		t.Errorf("teardown networks = %v; want %s", got, testNetwork)
	}
	if !slices.Equal(b.ns.unpinned, []string{testPinnedPath}) {
		t.Errorf("unpinned = %v; want pinned path", b.ns.unpinned)
	}
	assertTracked(t, b, "c1", false)
}

// TestDisconnectIgnoresOtherTenantsRecord verifies a caller cannot reach
// another tenant's pinned namespace just by naming its ContainerID/Network:
// the tracked record is only trusted when its inode matches the namespace
// the caller proved it is in.
func TestDisconnectIgnoresOtherTenantsRecord(t *testing.T) {
	b := newTestBackend(t)
	const otherPinned = "/run/netns/other-tenant"
	b.state.Put(trackedLinks("c1", otherPinned, testInode+1))

	if err := b.Disconnect(testPid, testTeardownOptions("c1")); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if !slices.Equal(b.network.teardowns, []string{testClientPath}) {
		t.Errorf("teardowns = %v; want only the caller's verified path", b.network.teardowns)
	}
	if len(b.ns.unpinned) != 0 {
		t.Errorf("unpinned = %v; want other tenant's pin untouched", b.ns.unpinned)
	}
	assertTracked(t, b, "c1", true)
}

func TestDisconnectUntracked(t *testing.T) {
	b := newTestBackend(t)

	if err := b.Disconnect(testPid, testTeardownOptions("c1")); err != nil {
		t.Fatalf("Disconnect() error = %v", err)
	}
	if !slices.Equal(b.network.teardowns, []string{testClientPath}) {
		t.Errorf("teardowns = %v; want verified path", b.network.teardowns)
	}
	if len(b.ns.unpinned) != 0 {
		t.Errorf("unpinned = %v; want none", b.ns.unpinned)
	}
}

func TestDisconnectRejectsUnverifiedNamespace(t *testing.T) {
	b := newTestBackend(t)
	b.state.Put(trackedLinks("c1", testPinnedPath, testInode))

	if err := b.Disconnect(testPid+1, testTeardownOptions("c1")); err == nil {
		t.Fatal("Disconnect() error = nil; want namespace verification failure")
	}
	if len(b.network.teardowns) != 0 || len(b.ns.unpinned) != 0 {
		t.Errorf("teardowns = %v, unpinned = %v; want nothing touched", b.network.teardowns, b.ns.unpinned)
	}
	assertTracked(t, b, "c1", true)
}

func TestDisconnectTeardownFailureKeepsRecord(t *testing.T) {
	b := newTestBackend(t)
	b.state.Put(trackedLinks("c1", testPinnedPath, testInode))
	b.network.teardownErr = errors.New("teardown failed")

	if err := b.Disconnect(testPid, testTeardownOptions("c1")); err == nil {
		t.Fatal("Disconnect() error = nil; want teardown failure")
	}
	if len(b.ns.unpinned) != 0 {
		t.Errorf("unpinned = %v; want pin kept for the cleanup loop", b.ns.unpinned)
	}
	assertTracked(t, b, "c1", true)
}

func TestCleanupDropsRecordEvenIfTeardownFails(t *testing.T) {
	b := newTestBackend(t)
	conn := trackedLinks("c1", testPinnedPath, testInode)
	b.state.Put(conn)
	b.network.teardownErr = errors.New("teardown failed")

	b.cleanupLinks(conn)

	if !slices.Equal(b.network.teardowns, []string{testPinnedPath}) {
		t.Errorf("teardowns = %v; want teardown attempted on pinned path", b.network.teardowns)
	}
	if !slices.Equal(b.ns.unpinned, []string{testPinnedPath}) {
		t.Errorf("unpinned = %v; want pinned path released", b.ns.unpinned)
	}
	assertTracked(t, b, "c1", false)
}

func TestCleanupCleansOnlyMissingConnections(t *testing.T) {
	b := newTestBackend(t)
	const alivePath, deadPath = "/run/netns/alive", "/run/netns/dead"
	b.state.Put(trackedLinks("alive", alivePath, testInode))
	b.state.Put(trackedLinks("dead", deadPath, testInode+1))
	b.ns.missing = map[string]bool{deadPath: true}

	b.cleanup()

	if !slices.Equal(b.network.teardowns, []string{deadPath}) {
		t.Errorf("teardowns = %v; want only %s", b.network.teardowns, deadPath)
	}
	if !slices.Equal(b.ns.unpinned, []string{deadPath}) {
		t.Errorf("unpinned = %v; want only %s", b.ns.unpinned, deadPath)
	}
	assertTracked(t, b, "alive", true)
	assertTracked(t, b, "dead", false)
}

// TestCleanupLoopSweepsImmediatelyOnStartup covers the startup-time orphan
// sweep: cleanupLoop must clean up an existing orphan before its first
// ticker fire, not wait a full cleanupInterval for it. The interval here is
// deliberately long so the ticker cannot be what triggers the cleanup within
// the test's deadline.
func TestCleanupLoopSweepsImmediatelyOnStartup(t *testing.T) {
	b := newTestBackend(t)
	b.cleanupInterval = time.Hour
	const deadPath = "/run/netns/dead"
	b.state.Put(trackedLinks("dead", deadPath, testInode))
	b.ns.missing = map[string]bool{deadPath: true}

	go b.cleanupLoop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, tracked := b.state.Get("dead", testNetwork); !tracked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("cleanupLoop did not sweep the orphan before its first tick")
}

func TestCleanupIntervalRejectsNonPositive(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		if err := CleanupInterval(interval)(&netmanBackend{}); err == nil {
			t.Errorf("CleanupInterval(%v) error = nil; want error", interval)
		}
	}

	backend := &netmanBackend{}
	if err := CleanupInterval(time.Minute)(backend); err != nil || backend.cleanupInterval != time.Minute {
		t.Errorf("CleanupInterval(1m) = %v, interval %v; want nil, 1m", err, backend.cleanupInterval)
	}
}
