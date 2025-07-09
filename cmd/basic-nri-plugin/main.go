package main

import (
	"context"
	"flag"
	"log"
	"strings"
	"time"

	"github.com/grosskur/basic-nri-plugin/internal/nriplugin"
)

const (
	defaultName   = "basic-nri-plugin"
	defaultIdx    = "00"
	defaultEvents = "RunPodSandbox,StopPodSandbox"

	defaultSocketPath = "/var/run/nri/nri.sock"

	registrationTimeout = 5 * time.Second
	requestTimeout      = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("main: exited with error: %v", err)
	}
	log.Println("main: exited successfully")
}

func run() error {
	flag.Set("logtostderr", "true")
	var (
		name   string
		idx    string
		events string

		socketPath string
	)
	flag.StringVar(&name, "name", defaultName, "plugin name to register")
	flag.StringVar(&idx, "idx", defaultIdx, "plugin index to register")
	flag.StringVar(&events, "events", defaultEvents, "plugin events to receive")
	flag.StringVar(&socketPath, "nri-socket-path", defaultSocketPath, "path to NRI socket")
	flag.Parse()

	p := nriplugin.New(nriplugin.Options{
		Name:   name,
		Index:  idx,
		Events: strings.Split(events, ","),

		SocketPath: socketPath,

		RegistrationTimeout: registrationTimeout,
		RequestTimeout:      requestTimeout,
	})

	ctx := context.Background()

	log.Println("main: starting")
	if err := p.Run(ctx); err != nil {
		return err
	}
	return nil
}
