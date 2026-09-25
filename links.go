package netman

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// ContainerLinks records enough about a successful Connect to let the
// reconciler later verify it is still alive and, if not, tear it down
// without needing a live client pid. NSPath is a persistent, bind-mounted
// namespace file created via podmannetns.NewNSFrom (not a /proc/<pid>/ns/net
// reference), so it stays valid and checkable long after the connecting
// client process — whose pid could otherwise be recycled by the kernel — is
// gone. Callers that drop a ContainerLinks record must release this pin via
// podmannetns.UnmountNS(NSPath).
type ContainerLinks struct {
	Options    SetupNetworkOptions `json:"options"`
	NSPath     string              `json:"ns_path"`
	NSInode    uint64              `json:"ns_inode"`
	Interfaces []string            `json:"interfaces"`
}

func (c ContainerLinks) Exists() bool {
	inode, err := GetNsInode(c.NSPath)
	if err != nil || inode != c.NSInode {
		return false
	}

	if len(c.Interfaces) == 0 {
		return true
	}

	namespace, err := netns.GetFromPath(c.NSPath)
	if err != nil {
		return false
	}
	defer namespace.Close()

	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		return false
	}
	defer handle.Close()

	for _, name := range c.Interfaces {
		if _, err := handle.LinkByName(name); err != nil {
			return false
		}
	}
	return true
}

func (c ContainerLinks) Isolate() error {
	var hostLink netlink.Link
	namespace, err := netns.GetFromPath(c.NSPath)
	if err != nil {
		return err
	}
	defer namespace.Close()

	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		return err
	}
	defer handle.Close()

	for _, name := range c.Interfaces {
		link, err := handle.LinkByName(name)
		if err == nil {
			peerIndex := link.Attrs().ParentIndex
			if peerIndex == 0 {
				return fmt.Errorf("interface %s has no veth peer index", link.Attrs().Name)
			}
			hostLink, err = netlink.LinkByIndex(peerIndex)
			if err == nil {
				err = netlink.LinkSetIsolated(hostLink, true)
			}
		}

		if err != nil {
			return err
		}
	}
	return nil
}

func (c *ContainerLinks) Key() string {
	return c.Options.ContainerID + "/" + c.Options.Network.Name
}

func (c ContainerLinks) String() string {
	return fmt.Sprintf("%s: %s", c.Options.ContainerName, c.Options.Network.Name)
}
