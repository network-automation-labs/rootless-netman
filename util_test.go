package netman

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
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

func TestTranslateContainerNSPath(t *testing.T) {
	wantPath := fmt.Sprintf("/proc/%d/ns/net", os.Getpid())
	gotPath, err := TranslateContainerNSPath(os.Getpid(), wantPath)
	if err != nil {
		t.Errorf("Failed to translate %s: %v", wantPath, err)
	} else if gotPath != wantPath {
		t.Errorf("TranslateContainerNSPath(%s) = %s; want %s", wantPath, gotPath, wantPath)
	}
}
