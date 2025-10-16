package netman

import (
	"go.podman.io/common/libnetwork/types"
)

type CreateNetworkOptions struct {
	types.Network
}

type LogPrinter interface {
	Printf(string, ...any)
	Println(...any)
}

type SetupNetworkOptions struct {
	ContainerID    string                  `json:"container_id"`
	ContainerName  string                  `json:"container_name"`
	ClientPid      int                     `json:"client_pid"`
	ContainerNS    uint64                  `json:"container_ns"`
	PortMappings   []types.PortMapping     `json:"port_mappings,omitempty"`
	Network        types.Network           `json:"network"`
	NetworkOptions types.PerNetworkOptions `json:"network_options"`
}

type TeardownNetworkOptions struct {
	SetupNetworkOptions
}
