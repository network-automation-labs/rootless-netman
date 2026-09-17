package netman

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func TestGetNextEthName(t *testing.T) {
	tests := []struct {
		linkNames []string
		expected  string
	}{
		{[]string{}, "eth0"},
		{[]string{"lo"}, "eth0"},
		{[]string{"lo", "eth0"}, "eth1"},
		{[]string{"lo", "eth0", "eth1"}, "eth2"},
		{[]string{"lo", "eth0", "eth2"}, "eth1"},
		{[]string{"lo", "eth1", "eth2"}, "eth0"},
		{[]string{"lo", "eth0", "eth1", "eth2"}, "eth3"},
		{[]string{"lo", "eth0", "eth1", "eth3"}, "eth2"},
	}

	for _, test := range tests {
		// copy the variable to avoid closure capture issue
		currentTest := test
		t.Run(currentTest.expected, func(t *testing.T) {
			result := GetNextEthName(currentTest.linkNames)
			if result != currentTest.expected {
				t.Errorf("GetNextEthName(%v) = %s; expected %s", currentTest.linkNames, result, currentTest.expected)
			}
		})
	}
}

func TestPeerCredentials(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "peercred.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to listen on %s: %v", socketPath, err)
	}
	defer listener.Close()

	acceptedCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		acceptedCh <- conn
	}()

	clientConn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to dial %s: %v", socketPath, err)
	}
	defer clientConn.Close()

	var serverConn net.Conn
	select {
	case serverConn = <-acceptedCh:
	case err := <-acceptErrCh:
		t.Fatalf("Failed to accept connection: %v", err)
	}
	defer serverConn.Close()

	cred, err := PeerCredentials(serverConn)
	if err != nil {
		t.Fatalf("PeerCredentials() returned error: %v", err)
	}

	if int(cred.Pid) != os.Getpid() {
		t.Errorf("PeerCredentials().Pid = %d; want %d", cred.Pid, os.Getpid())
	}
	if int(cred.Uid) != os.Getuid() {
		t.Errorf("PeerCredentials().Uid = %d; want %d", cred.Uid, os.Getuid())
	}
}

// TestFindNetInterfaceLinkAndIsolateHostPeer exercises the tenant-isolation
// helpers against a real veth pair and bridge: the tenant-side end lives in
// a throwaway namespace (as a container's would), the host-side end is
// attached to a bridge (as podman1 would be), and IsolateHostPeer must mark
// that host-side port isolated. Requires root/CAP_NET_ADMIN to create
// network namespaces, veths, and bridges, matching this daemon's own
// privilege requirements.
func TestFindNetInterfaceLinkAndIsolateHostPeer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root/CAP_NET_ADMIN to create network namespaces, veths, and bridges")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origin, err := netns.Get()
	if err != nil {
		t.Skipf("cannot get current network namespace: %v", err)
	}
	defer origin.Close()

	nsName := fmt.Sprintf("nm-test-%d", os.Getpid())
	tenantNs, err := netns.NewNamed(nsName)
	if err != nil {
		t.Skipf("creating a named network namespace requires CAP_NET_ADMIN: %v", err)
	}
	defer func() {
		tenantNs.Close()
		_ = netns.DeleteNamed(nsName)
	}()
	tenantNsPath := filepath.Join("/run/netns", nsName)

	// Still inside tenantNs here (NewNamed switches the calling thread into
	// it): create the veth pair as if it were a container's interface.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: "nmTestTenant"},
		PeerName:  "nmTestHost",
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("failed to create veth pair: %v", err)
	}

	tenantLink, err := netlink.LinkByName("nmTestTenant")
	if err != nil {
		t.Fatalf("failed to look up tenant-side veth: %v", err)
	}

	hostLink, err := netlink.LinkByName("nmTestHost")
	if err != nil {
		t.Fatalf("failed to look up host-side veth: %v", err)
	}
	if err := netlink.LinkSetNsFd(hostLink, int(origin)); err != nil {
		t.Fatalf("failed to move host-side veth to the origin namespace: %v", err)
	}

	// Switch back to the origin/host namespace: this is where the daemon
	// itself always runs. FindNetInterfaceLink/IsolateHostPeer must work
	// from here without the calling thread ever entering the tenant's ns.
	if err := netns.Set(origin); err != nil {
		t.Fatalf("failed to restore origin namespace: %v", err)
	}
	defer func() {
		if link, err := netlink.LinkByName("nmTestHost"); err == nil {
			_ = netlink.LinkDel(link)
		}
	}()

	bridge := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "nmTestBr0"}}
	if err := netlink.LinkAdd(bridge); err != nil {
		t.Fatalf("failed to create bridge: %v", err)
	}
	defer func() { _ = netlink.LinkDel(bridge) }()

	hostLink, err = netlink.LinkByName("nmTestHost")
	if err != nil {
		t.Fatalf("failed to re-fetch host-side veth: %v", err)
	}
	if err := netlink.LinkSetMaster(hostLink, bridge); err != nil {
		t.Fatalf("failed to attach host-side veth to bridge: %v", err)
	}

	if err := IsolateContainerInterfaces(tenantNsPath, []string{tenantLink.Attrs().Name}); err != nil {
		t.Fatalf("IsolateHostPeer() error = %v", err)
	}

	protinfo, err := netlink.LinkGetProtinfo(hostLink)
	if err != nil {
		t.Fatalf("LinkGetProtinfo() error = %v", err)
	}
	if !protinfo.Isolated {
		t.Error("host-side veth is not isolated after IsolateHostPeer()")
	}
}

func TestTranslateContainerNSPath(t *testing.T) {
	wantPath := fmt.Sprintf("/proc/%d/ns/net", os.Getpid())
	gotPath, err := TranslateContainerNSPath(os.Getpid(), wantPath)
	if err != nil {
		t.Errorf("Failed to translate %s: %v", wantPath, err)
	} else if gotPath != wantPath {
		t.Errorf("TranslateContainerNSPath(%s) = %s; want %s", wantPath, gotPath, wantPath)
	}
}
