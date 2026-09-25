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
	// Pin bind-mounts the namespace at path to a durable file and returns
	// the pinned path.
	Pin(path string) (string, error)
	// Unpin releases a path previously returned by Pin.
	Unpin(path string) error
	Inode(path string) (uint64, error)
	Isolate(links ContainerLinks) error
	Exists(links ContainerLinks) bool
}

type kernelNSOps struct{}

func (kernelNSOps) ResolvePath(clientPid int, inode uint64) (string, error) {
	return GetContainerNSPath(clientPid, inode)
}

func (kernelNSOps) Pin(path string) (string, error) {
	return podmannetns.NewNSFrom(path)
}

func (kernelNSOps) Unpin(path string) error {
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
