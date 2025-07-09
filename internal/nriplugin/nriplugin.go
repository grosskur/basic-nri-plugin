package nriplugin

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"

	"github.com/grosskur/basic-nri-plugin/third_party/nri/pkg/api"
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

	conn       net.Conn
	serverOpts []ttrpc.ServerOpt
	clientOpts []ttrpc.ClientOpts

	rpcm multiplex.Mux
	rpcl net.Listener
	rpcs *ttrpc.Server
	rpcc *ttrpc.Client

	runtime api.RuntimeService
	started bool
	doneC   chan struct{}
	srvErrC chan error
	cfgErrC chan error
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

func (p *Plugin) start(ctx context.Context) (retErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return fmt.Errorf("plugin already started")
	}
	p.doneC = make(chan struct{})

	var dialer net.Dialer
	var err error
	p.conn, err = dialer.DialContext(ctx, "unix", p.socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to NRI service: %w", err)
	}

	rpcm := multiplex.Multiplex(p.conn)
	defer func() {
		if retErr != nil {
			rpcm.Close()
			p.rpcm = nil
		}
	}()

	rpcl, err := rpcm.Listen(multiplex.PluginServiceConn)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			rpcl.Close()
			p.rpcl = nil
		}
	}()

	rpcs, err := ttrpc.NewServer(p.serverOpts...)
	if err != nil {
		return fmt.Errorf("failed to create ttrpc server: %w", err)
	}
	defer func() {
		if retErr != nil {
			rpcs.Close()
			p.rpcs = nil
		}
	}()

	api.RegisterPluginService(rpcs, p)

	conn, err := rpcm.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		return fmt.Errorf("failed to multiplex ttrpc client connection: %w", err)
	}

	clientOpts := []ttrpc.ClientOpts{
		ttrpc.WithOnClose(func() {
			p.connClosed()
		}),
	}
	rpcc := ttrpc.NewClient(conn, append(clientOpts, p.clientOpts...)...)
	defer func() {
		if retErr != nil {
			rpcc.Close()
			p.rpcc = nil
		}
	}()

	p.srvErrC = make(chan error, 1)
	p.cfgErrC = make(chan error, 1)

	go func(l net.Listener, doneC chan struct{}, srvErrC chan error) {
		srvErrC <- rpcs.Serve(ctx, l)
		close(doneC)
	}(rpcl, p.doneC, p.srvErrC)

	p.rpcm = rpcm
	p.rpcl = rpcl
	p.rpcs = rpcs
	p.rpcc = rpcc

	p.runtime = api.NewRuntimeClient(rpcc)

	if err = p.register(ctx); err != nil {
		p.close()
		return err
	}

	if err = <-p.cfgErrC; err != nil {
		return err
	}

	log.Printf("nriplugin: started: name=%s", p.Name())

	p.started = true
	return nil
}

func (p *Plugin) close() {
	if !p.started {
		return
	}

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
	if p.srvErrC != nil {
		<-p.doneC
	}

	p.started = false
	p.conn = nil
	p.syncReq = nil
}

func (p *Plugin) Run(ctx context.Context) error {
	var err error

	if err = p.start(ctx); err != nil {
		return err
	}

	err = <-p.srvErrC
	if err == ttrpc.ErrServerClosed {
		log.Printf("nriplugin: connection closed by ttrpc server: %v", err)
	}

	return err
}

func (p *Plugin) Name() string {
	return p.idx + "-" + p.name
}

func (p *Plugin) register(ctx context.Context) error {
	log.Printf("nriplugin: registering: name=%s", p.Name())

	ctx, cancel := context.WithTimeout(ctx, p.registrationTimeout)
	defer cancel()

	req := &api.RegisterPluginRequest{
		PluginName: p.name,
		PluginIdx:  p.idx,
	}
	if _, err := p.runtime.RegisterPlugin(ctx, req); err != nil {
		return fmt.Errorf("failed to register with runtime: %w", err)
	}

	return nil
}

func (p *Plugin) connClosed() {
	p.mu.Lock()
	p.close()
	p.mu.Unlock()
	log.Println("nriplugin: connection closed")
}

func (p *Plugin) Configure(ctx context.Context, req *api.ConfigureRequest) (*api.ConfigureResponse, error) {
	log.Printf("nriplugin: configuring: name=%s runtime=%s/%s...", p.Name(), req.RuntimeName, req.RuntimeVersion)

	p.registrationTimeout = time.Duration(req.RegistrationTimeout * int64(time.Millisecond))
	p.requestTimeout = time.Duration(req.RequestTimeout * int64(time.Millisecond))

	p.cfgErrC <- nil

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

	for _, pod := range syncReq.Pods {
		log.Printf("nriplugin: Synchronize: pod-uid=%+v", pod.Uid)
	}

	return &api.SynchronizeResponse{
		Update: []*api.ContainerUpdate{},
		More:   false,
	}, nil
}

func (p *Plugin) Shutdown(ctx context.Context, _ *api.Empty) (*api.Empty, error) {
	log.Println("hook: Shutdown")
	return &api.Empty{}, nil
}

func (p *Plugin) StateChange(ctx context.Context, evt *api.StateChangeEvent) (*api.Empty, error) {
	var err error

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

	return &api.Empty{}, err
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
