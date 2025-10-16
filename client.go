package netman

import (
	"net/rpc"

	"go.podman.io/common/libnetwork/types"
)

type Client struct {
	client *rpc.Client
}

func NewClient(socketPath string) (Netman, error) {
	client, err := rpc.Dial("unix", socketPath)
	return &Client{
		client: client,
	}, err
}

func (c *Client) Create(options CreateNetworkOptions) (CreateNetworkOptions, error) {
	response := CreateNetworkOptions{}

	err := c.client.Call("Netman.Inspect", options.Name, &response.Network)
	response.Name = options.Name
	response.ID = options.ID
	response.Driver = options.Driver

	return response, err
}

func (c *Client) Connect(options *SetupNetworkOptions) (types.StatusBlock, error) {
	statusBlock := &types.StatusBlock{}
	err := c.client.Call("Netman.Connect", options, statusBlock)
	return *statusBlock, err
}

func (c *Client) Disconnect(options *TeardownNetworkOptions) error {
	return c.client.Call("Netman.Disconnect", options, nil)
}

func (c *Client) Inspect(name string) (types.Network, error) {
	network := types.Network{}
	err := c.client.Call("Netman.Inspect", name, &network)
	return network, err
}
