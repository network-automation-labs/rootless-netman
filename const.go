package netman

var Version = "dev"

const (
	DriverName       = "rootless-netman"
	NetmanSocketDir  = "/run/systemd/rootless-netman"
	NetmanSocketPath = NetmanSocketDir + "/rootless-netman.sock"
)
