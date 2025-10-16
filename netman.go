package netman

import (
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

type DefaultNetman struct {
	types.ContainerNetwork
}

func NewNetman() (*DefaultNetman, error) {
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

	return &DefaultNetman{ContainerNetwork: netInterface}, err
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

func (n *DefaultNetman) Inspect(name string) (types.Network, error) {
	return n.NetworkInspect(name)
}

func (n *DefaultNetman) Disconnect(options *TeardownNetworkOptions) error {
	nspath, err := GetContainerNSPath(options.ClientPid, options.ContainerNS)
	if err == nil {
		return n.Teardown(nspath, types.TeardownOptions{NetworkOptions: getSetupOptions(&options.SetupNetworkOptions)})
	}
	return err
}

func (n *DefaultNetman) Connect(options *SetupNetworkOptions) (statusBlock types.StatusBlock, err error) {
	nspath, err := GetContainerNSPath(options.ClientPid, options.ContainerNS)
	if err == nil {
		var statusBlocks map[string]types.StatusBlock
		statusBlocks, err = n.Setup(nspath, types.SetupOptions{NetworkOptions: getSetupOptions(options)})
		statusBlock = statusBlocks[options.Network.Name]
	}

	return statusBlock, err
}
