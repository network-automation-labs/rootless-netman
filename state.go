package netman

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
)

type StateStore interface {
	Put(ContainerLinks)
	Get(containerID string, networkName string) (ContainerLinks, bool)
	Delete(ContainerLinks)
	GetLinks() map[string]ContainerLinks
}

// stateStore persists the set of currently connected tenants to disk so the
// daemon can reconcile orphaned connections across restarts, not just within
// a single process's lifetime.
type stateStore struct {
	mu      sync.Mutex
	path    string
	entries map[string]ContainerLinks
}

// loadStateStore reads the persisted state file at path, starting from an
// empty store if the file is missing or corrupt rather than failing daemon
// startup.
func loadStateStore(path string) (StateStore, error) {
	var s *stateStore
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		s = &stateStore{path: path, entries: map[string]ContainerLinks{}}
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			if len(data) > 0 {
				err = json.Unmarshal(data, &s.entries)
				if err != nil {
					logrus.Errorf("Ignoring corrupt netman state file %s: %v", path, err)
					s.entries = map[string]ContainerLinks{}
					err = nil
				}
			}
		} else if errors.Is(err, os.ErrNotExist) {
			err = nil
		}

	}
	return s, err
}

// put records conn under key and persists the change. Persistence failures
// are logged, not returned: bookkeeping is best-effort and must not fail the
// Connect RPC it's attached to.
func (s *stateStore) Put(conn ContainerLinks) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[conn.Key()] = conn
	s.persistLocked()
}

func (s *stateStore) Get(containerID string, networkName string) (ContainerLinks, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	links, found := s.entries[containerID+"/"+networkName]
	return links, found
}

// delete removes key and persists the change, if it was present.
func (s *stateStore) Delete(conn ContainerLinks) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[conn.Key()]; ok {
		delete(s.entries, conn.Key())
		s.persistLocked()
	}
}

// snapshot returns a copy of the tracked entries so callers (the reconciler)
// can range over them without holding the store lock during slow netlink
// calls.
func (s *stateStore) GetLinks() map[string]ContainerLinks {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]ContainerLinks, len(s.entries))
	maps.Copy(out, s.entries)
	return out
}

// persistLocked writes the current entries to disk atomically via a
// temp-file-then-rename. The caller must hold s.mu.
func (s *stateStore) persistLocked() {
	data, err := json.Marshal(s.entries)

	if err != nil {
		logrus.Errorf("Failed to marshal netman state: %v", err)
		return
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		logrus.Errorf("Failed to write netman state file %s: %v", tmp, err)
		return
	}

	if err := os.Rename(tmp, s.path); err != nil {
		logrus.Errorf("Failed to persist netman state file %s: %v", s.path, err)
	}
}
