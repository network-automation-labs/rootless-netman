package netman

import (
	podmannetns "go.podman.io/common/pkg/netns"
)

// nsOps is every kernel-facing namespace operation netmanBackend depends on.
// It exists so the backend's authorization, rollback, and reconciliation
// logic can be unit tested without root; kernelNSOps is the only production
// implementation.
type nsOps interface {
	// ResolvePath returns the netns path of clientPid, but only if that
	// namespace's inode matches inode.
	ResolvePath(clientPid int, inode uint64) (string, error)
	// Mount bind-mounts the namespace at path to a durable file and returns
	// the pinned path.
	Mount(path string) (string, error)
	// Unmount releases a path previously returned by Pin.
	Unmount(path string) error
	Inode(path string) (uint64, error)
	Isolate(links ContainerLinks) error
	Exists(links ContainerLinks) bool
}

type kernelNSOps struct{}

func (kernelNSOps) ResolvePath(clientPid int, inode uint64) (string, error) {
	return GetContainerNSPath(clientPid, inode)
}

func (kernelNSOps) Mount(path string) (string, error) {
	return podmannetns.NewNSFrom(path)
}

func (kernelNSOps) Unmount(path string) error {
	return podmannetns.UnmountNS(path)
}

func (kernelNSOps) Inode(path string) (uint64, error) {
	return GetNsInode(path)
}

func (kernelNSOps) Isolate(links ContainerLinks) error {
	return links.Isolate()
}

func (kernelNSOps) Exists(links ContainerLinks) bool {
	return links.Exists()
}
