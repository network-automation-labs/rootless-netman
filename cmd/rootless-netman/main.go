package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/network-automation-labs/netman"
	"github.com/sirupsen/logrus"
)

var versionOpt = false
var debugOpt = false
var serverOpt = false
var textModeOpt = false

func init() {
	flag.BoolVar(&versionOpt, "version", false, "print version and exit")
	flag.BoolVar(&debugOpt, "debug", false, "turn on debug logging")
	flag.BoolVar(&serverOpt, "server", false, "run in server mode")
	flag.BoolVar(&textModeOpt, "text", false, "run in text mode")
}

func server() {
	server, err := netman.NewServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create server: %v\n", err)
		os.Exit(1)
	}
	err = server.ServeSystemd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to serve systemd socket: %v\n", err)
		os.Exit(1)
	}
}

func plugin(args ...string) {
	p := netman.NewPlugin(os.Stdin, os.Stdout, netman.NetmanSocketPath)

	if len(args) == 0 {
		p.Fail("No command provided")
	}
	cmd := args[0]
	logrus.Println("Running command:", cmd)

	nsPath := ""
	if cmd == "setup" || cmd == "teardown" {
		if len(args) < 2 {
			p.Fail("No namespace path provided")
		}
		nsPath = args[1]
		logrus.Debugf("Namespace path: %s", nsPath)
	}

	switch cmd {
	case "create":
		p.Inspect()
	case "inspect":
		if !textModeOpt {
			p.Fail("Inspect command only supported in text mode")
		} else if len(args) < 2 {
			p.Fail("No network name provided")
		}
		network, err := p.Netman.Inspect(args[1])
		var output []byte
		if err == nil {
			output, err = json.MarshalIndent(network, "", "  ")
		}
		if err == nil {
			fmt.Println(string(output))
		} else {
			fmt.Fprintf(os.Stderr, "Failed to retrieve network info: %v\n", err)
		}
	case "setup":
		p.Setup(nsPath)
	case "teardown":
		p.Teardown(nsPath)
	case "info":
		p.Info()
	default:
		p.Fail("Unknown command: " + cmd)
	}
}

func main() {
	flag.Parse()
	if versionOpt {
		fmt.Printf("rootless-netman version %s\n", netman.Version)
		os.Exit(0)
	}

	if debugOpt {
		logrus.SetLevel(logrus.DebugLevel)
	}

	if serverOpt {
		server()
	} else {
		args := flag.Args()
		plugin(args[0:]...)
	}
}
