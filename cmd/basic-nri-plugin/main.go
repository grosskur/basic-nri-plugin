package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
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
	shutdownDelay       = 2 * time.Second
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ctxStop, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		log.Println("main: plugin starting")
		if err := p.Run(ctx); err != nil {
			log.Printf("main: plugin finished with error: %v", err)
		} else {
			log.Println("main: plugin finished successfully")
		}
		wg.Done()
	}()

	select {
	case <-ctxStop.Done():
		break
	case <-ctx.Done():
		break
	}

	log.Println("main: received shutdown signal, waiting to shut down: delay=%s", shutdownDelay)
	time.Sleep(shutdownDelay)

	log.Println("main: shutting down")
	cancel()
	p.Close()
	wg.Wait()

	return nil
}
