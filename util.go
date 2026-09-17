package netman

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

func IsolateContainerInterfaces(nsPath string, containerInterfaceNames []string) error {
	var hostLink netlink.Link
	namespace, err := netns.GetFromPath(nsPath)
	if err != nil {
		return err
	}
	defer namespace.Close()

	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		return err
	}
	defer handle.Close()

	for _, name := range containerInterfaceNames {
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

func GetNextEthName(linkNames []string) (ethName string) {
	indices := []int{}

	for _, linkName := range linkNames {
		if strings.HasPrefix(linkName, "eth") {
			index, err := strconv.Atoi(linkName[3:])
			if err == nil {
				indices = append(indices, index)
			}
		}
	}

	sort.Ints(indices)
	lastIndex := -1
	for _, index := range indices {
		if index != lastIndex+1 {
			break
		}
		lastIndex = index
	}
	lastIndex++
	ethName = "eth" + strconv.Itoa(lastIndex)

	return ethName
}

func GetContainerNSPath(pid int, inode uint64) (string, error) {
	pattern := fmt.Sprintf("/proc/%d/ns/net", pid)
	matches, _ := filepath.Glob(pattern)

	for _, path := range matches {
		linkStr, err := os.Readlink(path)
		if err == nil {
			if strings.HasPrefix(linkStr, "net:[") {
				linkStr = linkStr[5:]
				linkStr = linkStr[:len(linkStr)-1]
				link, _ := strconv.Atoi(linkStr)
				if uint64(link) == inode {
					return path, nil
				}
			}
		} else if !errors.Is(err, os.ErrPermission) {
			logrus.Warn("Error reading", path, ":", err.Error())
		}
	}
	return "", fmt.Errorf("failed to find matching process netns for inode %d", inode)
}

func GetNsInode(nsPath string) (inode uint64, err error) {
	fileInfo, err := os.Stat(nsPath)
	if err == nil {
		stat, ok := fileInfo.Sys().(*syscall.Stat_t)
		if ok {
			inode = stat.Ino
		} else {
			err = fmt.Errorf("failed to assert Sys() to *syscall.Stat_t")
		}
	}
	return
}

// PeerCredentials returns the kernel-verified UID/PID/GID of the process on
// the other end of a Unix domain socket connection, via SO_PEERCRED. This
// must be used instead of trusting any PID/UID a client sends over the wire.
func PeerCredentials(conn net.Conn) (*syscall.Ucred, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("connection is not a unix socket")
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return nil, err
	}

	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return nil, err
	}
	return cred, credErr
}

func TranslateContainerNSPath(pid int, containerNsPath string) (string, error) {
	inode, err := GetNsInode(containerNsPath)
	if err == nil {
		containerNsPath, err = GetContainerNSPath(pid, inode)
	}
	return containerNsPath, err
}
