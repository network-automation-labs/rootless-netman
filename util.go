package netman

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type NullLogger int

func (NullLogger) Println(...any)        {}
func (NullLogger) Printf(string, ...any) {}

var Logger LogPrinter = NullLogger(0)

func GetLinkNames(namespace netns.NsHandle) (linkNames []string, err error) {
	linkNames = []string{}
	handle, err := netlink.NewHandleAt(namespace)
	if err == nil {
		var links []netlink.Link
		links, err = handle.LinkList()
		for _, link := range links {
			linkNames = append(linkNames, link.Attrs().Name)
		}
	}
	return linkNames, err
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
			Logger.Println("Error reading", path, ":", err.Error())
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

func TranslateContainerNSPath(pid int, containerNsPath string) (string, error) {
	inode, err := GetNsInode(containerNsPath)
	if err == nil {
		containerNsPath, err = GetContainerNSPath(pid, inode)
	}
	return containerNsPath, err
}
