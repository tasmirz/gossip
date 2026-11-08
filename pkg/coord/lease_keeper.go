package coord

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPAdapter owns background renewal for claims made through its server.
// Close it when its transport exits, before closing the backend.
type MCPAdapter struct {
	Server         *server.MCPServer
	keeper         *leaseKeeper
	outbox         *Outbox
	presenceCancel context.CancelFunc
	presenceDone   chan struct{}
	presenceOnce   sync.Once
}

func NewMCPAdapter(store Backend, project, token string) *MCPAdapter {
	return newMCPAdapter(store, project, token, nil, nil)
}

func NewMCPAdapterWithHostCapabilities(store Backend, project, token string, outbox *Outbox, hostNames []string) (*MCPAdapter, error) {
	hostCapabilities, err := HostReportedCapabilities(hostNames)
	if err != nil {
		return nil, err
	}
	return newMCPAdapter(store, project, token, outbox, hostCapabilities), nil
}

func newMCPAdapter(store Backend, project, token string, outbox *Outbox, hostCapabilities []AgentCapability) *MCPAdapter {
	k := newLeaseKeeper(func(ctx context.Context, id string, attempt int, lease time.Duration) error {
		_, err := store.RenewLease(ctx, project, token, id, attempt, lease)
		return err
	})
	a := &MCPAdapter{keeper: k, outbox: outbox}
	a.Server = mcpServer(store, project, token, k, outbox, func(client mcp.Implementation) {
		a.presenceOnce.Do(func() {
			a.startPresence(store, project, token, hostCapabilities, MCPClientInfo{Name: client.Name, Version: client.Version})
		})
	})
	return a
}

// NewMCPAdapterWithOutbox enables explicit queued messages on this adapter.
func NewMCPAdapterWithOutbox(store Backend, project, token string, outbox *Outbox) *MCPAdapter {
	if outbox != nil {
		a := newMCPAdapter(store, project, token, outbox, nil)
		outbox.setReportDelivered(func(taskID string, attempt int) { a.keeper.stop(taskID, attempt) })
		return a
	}
	return NewMCPAdapter(store, project, token)
}
func (a *MCPAdapter) Close() {
	if a.presenceCancel != nil {
		a.presenceCancel()
		<-a.presenceDone
	}
	a.keeper.close()
	if a.outbox != nil {
		a.outbox.Close()
	}
}

func (a *MCPAdapter) startPresence(backend Backend, project, token string, hostCapabilities []AgentCapability, client MCPClientInfo) {
	instanceID, err := randomID("agent_session_")
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.presenceCancel = cancel
	a.presenceDone = make(chan struct{})
	capabilities := []AgentCapability{{Name: "mcp_tools", Source: "adapter"}, {Name: "git_workspace_inspection", Source: "adapter"}, {Name: "lease_renewal", Source: "adapter"}}
	if a.outbox != nil {
		capabilities = append(capabilities, AgentCapability{Name: "durable_outbox", Source: "adapter"})
	}
	capabilities = append(capabilities, hostCapabilities...)
	go func() {
		defer close(a.presenceDone)
		heartbeat := func() {
			request, done := context.WithTimeout(ctx, 5*time.Second)
			_ = backend.HeartbeatAgentWithMetadata(request, project, token, instanceID, capabilities, client)
			done()
		}
		heartbeat()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				request, done := context.WithTimeout(context.Background(), 5*time.Second)
				_ = backend.EndAgentSession(request, project, token, instanceID)
				done()
				return
			case <-ticker.C:
				heartbeat()
			}
		}
	}()
}

type renewal struct {
	cancel  context.CancelFunc
	attempt int
}
type leaseKeeper struct {
	mu     sync.Mutex
	closed bool
	active map[string]*renewal
	wg     sync.WaitGroup
	renew  func(context.Context, string, int, time.Duration) error
}

func newLeaseKeeper(renew func(context.Context, string, int, time.Duration) error) *leaseKeeper {
	return &leaseKeeper{active: make(map[string]*renewal), renew: renew}
}
func (k *leaseKeeper) track(id string, attempt int, lease time.Duration) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	if old := k.active[id]; old != nil {
		old.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	current := &renewal{cancel: cancel, attempt: attempt}
	k.active[id] = current
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		defer func() {
			k.mu.Lock()
			if k.active[id] == current {
				delete(k.active, id)
			}
			k.mu.Unlock()
		}()
		interval := lease / 3
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			request, done := context.WithTimeout(ctx, interval)
			err := k.renew(request, id, attempt, lease)
			done()
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			timer.Reset(interval)
		}
	}()
}
func (k *leaseKeeper) stop(id string, attempt int) {
	if k == nil {
		return
	}
	k.mu.Lock()
	if r := k.active[id]; r != nil && r.attempt == attempt {
		delete(k.active, id)
		r.cancel()
	}
	k.mu.Unlock()
}
func (k *leaseKeeper) close() {
	k.mu.Lock()
	k.closed = true
	for id, r := range k.active {
		r.cancel()
		delete(k.active, id)
	}
	k.mu.Unlock()
	k.wg.Wait()
}
