package netman

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.podman.io/common/libnetwork/types"
)

func TestStateStorePutDeleteReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")

	store, err := loadStateStore(path)
	if err != nil {
		t.Fatalf("loadStateStore() error = %v", err)
	}

	got := store.GetLinks()
	if len(got) != 0 {
		t.Fatalf("GetLinks() on a fresh store = %v; want empty", len(got))
	}

	conn := ContainerLinks{
		Options: SetupNetworkOptions{
			ContainerID:   "abc123",
			ContainerName: "test-container",
			Network:       types.Network{Name: "external"},
		},
		NSPath:     "/run/netns/nm-test",
		NSInode:    42,
		Interfaces: []string{"eth1"},
	}
	store.Put(conn)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not created: %v", err)
	}

	store, err = loadStateStore(path)
	reloaded := store.(*stateStore)
	if err != nil {
		t.Fatalf("loadStateStore() after put error = %v", err)
	}
	got = reloaded.GetLinks()
	if len(got) != 1 {
		t.Fatalf("GetLinks() after reload = %+v; want 1 entry", got)
	}
	key := conn.Options.ContainerID + "/" + conn.Options.Network.Name
	gotConn, found := store.Get(conn.Options.ContainerID, conn.Options.Network.Name)
	if !found {
		t.Error("Did not find connection in the state store")
	}

	if !reflect.DeepEqual(gotConn, conn) {
		t.Errorf("GetLinks()[%q] = %+v; want %+v", key, got, conn)
	}

	reloaded.Delete(conn)
	got = reloaded.GetLinks()
	if len(got) != 0 {
		t.Fatalf("GetLinks() after delete = %v; want empty", got)
	}

	store, err = loadStateStore(path)
	if err != nil {
		t.Fatalf("loadStateStore() after delete error = %v", err)
	}
	got = store.GetLinks()
	if len(got) != 0 {
		t.Fatalf("GetLinks() after reload post-delete = %v; want empty", got)
	}
}

func TestStateStoreCorruptFileRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("failed to seed corrupt state file: %v", err)
	}

	store, err := loadStateStore(path)
	if err != nil {
		t.Fatalf("loadStateStore() on corrupt file error = %v; want recovery to an empty store", err)
	}
	got := store.GetLinks()
	if len(got) != 0 {
		t.Fatalf("GetLinks() after loading corrupt file = %v; want empty", got)
	}
}

func TestStateStoreMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "state.json")

	store, err := loadStateStore(path)
	if err != nil {
		t.Fatalf("loadStateStore() on missing file error = %v", err)
	}
	got := store.GetLinks()
	if len(got) != 0 {
		t.Fatalf("GetLinks() on missing file = %v; want empty", got)
	}
}
