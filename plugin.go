package netman

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/sirupsen/logrus"
	"go.podman.io/common/libnetwork/types"
)

type Plugin struct {
	reader *json.Decoder
	writer *json.Encoder
	Netman Netman
}

func NewPlugin(reader io.Reader, writer io.Writer, socketPath string) *Plugin {
	p := &Plugin{
		reader: json.NewDecoder(reader),
		writer: json.NewEncoder(writer),
	}
	var err error
	p.Netman, err = NewClient(socketPath)
	if err != nil {
		p.Fail(err)
	}
	return p
}

func (p *Plugin) readConfig(v any) {
	err := p.reader.Decode(v)
	if err != nil {
		logrus.Errorf("Failed to decode config: %v", err)
		p.Fail("Failed to decode config: " + err.Error())
	}
}

func (p *Plugin) respond(value interface{}) {
	if err := p.writer.Encode(value); err != nil {
		logrus.Errorf("Failed to encode response:", err.Error())
		os.Exit(1)
	}
}

func (p *Plugin) Fail(v ...any) {
	logrus.Println("Failing with message", v)
	p.respond(map[string]string{"error": fmt.Sprint(v...)})
	os.Exit(1)
}

func (p *Plugin) getContainerNS(nsPath string) (pid int, inode uint64) {
	pid = os.Getpid()
	inode, err := GetNsInode(nsPath)
	if err != nil {
		p.Fail(err)
	}
	return
}

func (p *Plugin) Inspect() {
	config := types.Network{}
	p.readConfig(&config)
	network, err := p.Netman.Inspect(config.Name)
	if err == nil {
		network.Name = config.Name
		network.ID = config.ID
		network.Driver = config.Driver
		network.NetworkInterface = ""
		p.respond(network)
	} else {
		p.Fail(err)
	}
}

func (p *Plugin) Setup(nsPath string) {
	var err error
	config := &SetupNetworkOptions{}
	p.readConfig(config)
	config.ClientPid, config.ContainerNS = p.getContainerNS(nsPath)

	var statusBlock types.StatusBlock
	// Switch to the target network namespace so that
	// the server process can find the namespace in the
	// /proc filesystem.
	err = ns.WithNetNSPath(nsPath, func(_ ns.NetNS) error {
		logrus.Println("Connecting", config.ContainerName, "to", config.Network.Name, "in namespace", nsPath)
		statusBlock, err = p.Netman.Connect(config)
		return err
	})

	if err != nil {
		p.Fail(err)
	}
	p.respond(statusBlock)
}

func (p *Plugin) Teardown(nsPath string) {
	var err error
	config := &TeardownNetworkOptions{}
	p.readConfig(config)
	config.ClientPid, config.ContainerNS = p.getContainerNS(nsPath)

	Logger.Println("Disconnecting", config.ContainerName, "from", config.Network.Name, "in namespace", nsPath)
	err = p.Netman.Disconnect(config)
	if err != nil {
		p.Fail(err)
	}
}

func (p *Plugin) Info() {
	info := map[string]string{"version": Version, "name": DriverName}
	p.respond(info)
}
