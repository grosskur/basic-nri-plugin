package nriplugin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"

	"github.com/grosskur/basic-nri-plugin/third_party/nri/pkg/api"
)

const (
	runBackoff = 2 * time.Second
)

type Options struct {
	Name       string
	Index      string
	SocketPath string

	Events []string

	RegistrationTimeout time.Duration
	RequestTimeout      time.Duration
}

type Plugin struct {
	mu sync.Mutex

	events api.EventMask

	name       string
	idx        string
	socketPath string

	rpcm multiplex.Mux
	rpcl net.Listener
	rpcs *ttrpc.Server
	rpcc *ttrpc.Client

	syncReq *api.SynchronizeRequest

	registrationTimeout time.Duration
	requestTimeout      time.Duration
}

func New(opts Options) *Plugin {
	return &Plugin{
		name:       opts.Name,
		idx:        opts.Index,
		socketPath: opts.SocketPath,
		events:     api.MustParseEventMask(opts.Events...),

		registrationTimeout: opts.RegistrationTimeout,
		requestTimeout:      opts.RequestTimeout,
	}
}

func (p *Plugin) Close() {
	if p.rpcl != nil {
		p.rpcl.Close()
	}
	if p.rpcs != nil {
		p.rpcs.Close()
	}
	if p.rpcc != nil {
		p.rpcc.Close()
	}
	if p.rpcm != nil {
		p.rpcm.Close()
	}
	p.syncReq = nil
}

func (p *Plugin) Run(ctx context.Context) error {
	for {
		if err := p.run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			log.Printf("nriplugin: error running plugin: %v", err)
		}
		log.Printf("nriplugin: sleeping %s", runBackoff)
		time.Sleep(runBackoff)
	}
}

func (p *Plugin) run(ctx context.Context) error {
	// Dial and multiplex.
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", p.socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to NRI service: %w", err)
	}
	p.rpcm = multiplex.Multiplex(conn)

	// Create server.
	p.rpcs, err = ttrpc.NewServer()
	if err != nil {
		return fmt.Errorf("failed to create ttrpc server: %w", err)
	}
	api.RegisterPluginService(p.rpcs, p)

	// Create client.
	mconn, err := p.rpcm.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		return fmt.Errorf("failed to multiplex ttrpc client connection: %w", err)
	}
	clientOpts := []ttrpc.ClientOpts{
		ttrpc.WithOnClose(func() {
			p.mu.Lock()
			p.Close()
			p.mu.Unlock()
			log.Println("nriplugin: connection closed")
		}),
	}
	p.rpcc = ttrpc.NewClient(mconn, clientOpts...)

	// Listen and serve.
	p.rpcl, err = p.rpcm.Listen(multiplex.PluginServiceConn)
	if err != nil {
		return err
	}

	doneCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		if err := p.rpcs.Serve(ctx, p.rpcl); err != nil {
			log.Printf("nriplugin: RPC server error: %v", err)
		}
		close(doneCh)
		wg.Done()
	}()

	// Use client to register.
	if err := p.register(ctx); err != nil {
		return err
	}

	// Wait.
	select {
	case <-ctx.Done():
	case <-doneCh:
	}
	wg.Wait()

	return ctx.Err()
}

func (p *Plugin) Name() string {
	return p.idx + "-" + p.name
}

func (p *Plugin) register(ctx context.Context) error {
	log.Printf("nriplugin: registering: name=%s", p.Name())

	ctx, cancel := context.WithTimeout(ctx, p.registrationTimeout)
	defer cancel()

	runtime := api.NewRuntimeClient(p.rpcc)
	req := &api.RegisterPluginRequest{
		PluginName: p.name,
		PluginIdx:  p.idx,
	}
	if _, err := runtime.RegisterPlugin(ctx, req); err != nil {
		return fmt.Errorf("failed to register with NRI/Runtime: %w", err)
	}

	return nil
}

func (p *Plugin) Configure(ctx context.Context, req *api.ConfigureRequest) (*api.ConfigureResponse, error) {
	log.Printf("nriplugin: configuring: name=%s runtime=%s/%s...", p.Name(), req.RuntimeName, req.RuntimeVersion)

	p.registrationTimeout = time.Duration(req.RegistrationTimeout * int64(time.Millisecond))
	p.requestTimeout = time.Duration(req.RequestTimeout * int64(time.Millisecond))

	return &api.ConfigureResponse{
		Events: int32(p.events),
	}, nil
}

func (p *Plugin) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	if req.More {
		return p.collectSync(req)
	}
	return p.deliverSync(ctx, req)
}

func (p *Plugin) collectSync(req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.syncReq == nil {
		p.syncReq = req
	} else {
		p.syncReq.Pods = append(p.syncReq.Pods, req.Pods...)
		p.syncReq.Containers = append(p.syncReq.Containers, req.Containers...)
	}
	return &api.SynchronizeResponse{More: req.More}, nil
}

func (p *Plugin) deliverSync(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	p.mu.Lock()
	syncReq := p.syncReq
	p.syncReq = nil
	p.mu.Unlock()

	if syncReq == nil {
		syncReq = req
	} else {
		syncReq.Pods = append(syncReq.Pods, req.Pods...)
		syncReq.Containers = append(syncReq.Containers, req.Containers...)
	}

	update, err := p.handleSync(ctx, syncReq.Pods, syncReq.Containers)
	return &api.SynchronizeResponse{
		Update: update,
		More:   false,
	}, err
}

func (p *Plugin) handleSync(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	for _, pod := range pods {
		log.Printf("nriplugin: Synchronize: pod-uid=%+v", pod.Uid)
	}
	return []*api.ContainerUpdate{}, nil
}

func (p *Plugin) Shutdown(ctx context.Context, _ *api.Empty) (*api.Empty, error) {
	log.Println("nriplugin: Shutdown")
	return &api.Empty{}, nil
}

func (p *Plugin) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	log.Printf("nriplugin: CreateContainer: pod-uid=%s", req.Pod.Uid)
	return &api.CreateContainerResponse{}, nil
}

func (p *Plugin) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	log.Printf("nriplugin: StopContainer: pod-uid=%s", req.Pod.Uid)
	return &api.StopContainerResponse{}, nil
}

func (p *Plugin) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	log.Printf("nriplugin: UpdateContainer: pod-uid=%s", req.Pod.Uid)
	return &api.UpdateContainerResponse{}, nil
}

func (p *Plugin) StateChange(ctx context.Context, evt *api.StateChangeEvent) (*api.Empty, error) {
	switch evt.Event {
	case api.Event_RUN_POD_SANDBOX:
		log.Printf("nriplugin: StateChange: event=RunPodSandbox pod-uid=%s", evt.Pod.Uid)
	case api.Event_STOP_POD_SANDBOX:
		log.Printf("nriplugin: StateChange: event=StopPodSandbox pod-uid=%s", evt.Pod.Uid)
	case api.Event_REMOVE_POD_SANDBOX:
		log.Printf("nriplugin: StateChange: event=RemovePodSandbox pod-uid=%s", evt.Pod.Uid)
	case api.Event_POST_CREATE_CONTAINER:
		log.Printf("nriplugin: StateChange: event=PostCreateContainer pod-uid=%s container-id=%s", evt.Pod.Uid, evt.Container.Id)
	case api.Event_START_CONTAINER:
		log.Printf("nriplugin: StateChange: event=StartContainer pod-uid=%s container-id=%s", evt.Pod.Uid, evt.Container.Id)
	case api.Event_POST_START_CONTAINER:
		log.Printf("nriplugin: StateChange: event=PostStartContainer pod-uid=%s container-id=%s", evt.Pod.Uid, evt.Container.Id)
	case api.Event_POST_UPDATE_CONTAINER:
		log.Printf("nriplugin: StateChange: event=PostUpdateContainer pod-uid=%s container-id=%s", evt.Pod.Uid, evt.Container.Id)
	case api.Event_REMOVE_CONTAINER:
		log.Printf("nriplugin: StateChange: event=RemoveContainer pod-uid=%s container-id=%s", evt.Pod.Uid, evt.Container.Id)
	default:
		log.Printf("nriplugin: StateChange: unknown event: %s", evt.Event)
	}
	return &api.Empty{}, nil
}
