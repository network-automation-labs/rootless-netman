package netman

import "time"

var Version = "dev"

const (
	DriverName       = "rootless-netman"
	NetmanSocketDir  = "/run/systemd/rootless-netman"
	NetmanSocketPath = NetmanSocketDir + "/rootless-netman.sock"

	NetmanStateDir  = "/run/rootless-netman"
	NetmanStatePath = NetmanStateDir + "/state.json"

	DefaultCleanupInterval = 60 * time.Second
)
