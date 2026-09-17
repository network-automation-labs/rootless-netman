package netman

import (
	"github.com/sirupsen/logrus"
	"go.podman.io/common/libnetwork/network"
	"go.podman.io/common/libnetwork/types"
	"go.podman.io/common/pkg/config"

	"go.podman.io/storage"
)

type Netman interface {
	Inspect(name string) (types.Network, error)
	Connect(options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error)
	Disconnect(options *TeardownNetworkOptions) error
}

type Backend interface {
	Inspect(name string) (types.Network, error)
	Connect(clientPid int, options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error)
	Disconnect(clientPid int, options *TeardownNetworkOptions) error
}

type netmanBackend struct {
	types.ContainerNetwork
}

func NewNetman() (*netmanBackend, error) {
	storageOptions, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, err
	}

	store, err := storage.GetStore(storageOptions)
	if err != nil {
		return nil, err
	}

	conf, err := config.Default()
	if err != nil {
		return nil, err
	}

	_, netInterface, err := network.NetworkBackend(store, conf, false)

	return &netmanBackend{ContainerNetwork: netInterface}, err
}

func getSetupOptions(options *SetupNetworkOptions) types.NetworkOptions {
	return types.NetworkOptions{
		ContainerID:   options.ContainerID,
		ContainerName: options.ContainerName,
		Networks: map[string]types.PerNetworkOptions{
			options.Network.Name: options.NetworkOptions,
		},
	}
}

func (n *netmanBackend) Inspect(name string) (types.Network, error) {
	return n.NetworkInspect(name)
}

func (n *netmanBackend) Disconnect(clientPid int, options *TeardownNetworkOptions) error {
	nspath, err := GetContainerNSPath(clientPid, options.ContainerNS)
	if err == nil {
		networkOptions := getSetupOptions(&options.SetupNetworkOptions)
		logrus.Debugf("Disconnecting %s from %s: %+v", options.ContainerName, options.Network.Name, networkOptions)
		return n.Teardown(nspath, types.TeardownOptions{NetworkOptions: networkOptions})
	} else {
		logrus.Errorf("Failed disconnect container %v", err)
	}
	return err
}

func (n *netmanBackend) Connect(clientPid int, options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error) {
	nspath, err := GetContainerNSPath(clientPid, options.ContainerNS)
	if err == nil {
		if logrus.IsLevelEnabled(logrus.DebugLevel) {
			logrus.Debugf("Connecting %s to %s", options.ContainerName, options.Network.Name)
			logrus.Debugf("Connect options received: %+v", options)
		}
		var statusBlocks map[string]types.StatusBlock
		statusBlocks, err = n.Setup(nspath, types.SetupOptions{NetworkOptions: getSetupOptions(options)})
		statusBlock = statusBlocks[options.Network.Name]
	}
	if err != nil {
		logrus.Errorf("Failed to connect container %v", err)
	}
	return statusBlock, err
}
