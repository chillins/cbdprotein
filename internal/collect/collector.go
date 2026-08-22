package collect

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/goccy/go-json"
	"github.com/chillins/cbdprotein/internal/event"
	"github.com/chillins/cbdprotein/internal/storage"
)

type (
	Options struct {
		Type string
		Ext  string

		Store    storage.Storage
		EventHub *event.Hub

		// Hook is called once per collection that reaches a terminal status.
		// Snapshots reprocessed at startup do not trigger it.
		Hook func(*Entry)
	}

	Collector struct {
		typ string
		ext string

		store     storage.Storage
		eventHub  *event.Hub
		processor Processor
		hook      func(*Entry)

		mu   *sync.RWMutex
		data map[string]*Entry
	}

	Entry struct {
		Snapshot *Snapshot
		Status   Status
		Message  string
	}
	Status string
)

const (
	StatusOk      Status = "ok"
	StatusFail    Status = "fail"
	StatusPending Status = "pending"
)

func New(processor Processor, opts *Options) (*Collector, error) {
	c := &Collector{
		typ: opts.Type,
		ext: opts.Ext,

		store:     opts.Store,
		eventHub:  opts.EventHub,
		processor: newCachedProcessor(processor, opts.Store),
		hook:      opts.Hook,

		mu:   &sync.RWMutex{},
		data: map[string]*Entry{},
	}

	rawSnapshots, err := c.store.GetAll(c.typ)
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshots: %w", err)
	}

	for _, raw := range rawSnapshots {
		snapshot := &Snapshot{store: c.store}
		if err := snapshot.unmarshal(raw); err != nil {
			log.Printf("[!] unmarshalling snapshot failed: %v", err)
			continue
		}
		go c.runProcessor(snapshot, false)
	}

	return c, nil
}

func (c *Collector) updateStatus(snapshot *Snapshot, status Status, msg string) *Entry {
	entry := &Entry{
		Snapshot: snapshot,
		Status:   status,
		Message:  msg,
	}

	eventData, err := json.Marshal(entry)
	if err != nil {
		log.Printf("failed to serialize event: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[snapshot.ID] = entry

	if eventData != nil {
		c.eventHub.Publish(eventData)
	}

	return entry
}

// finalize records a terminal status. When notify is set, the completion hook
// fires exactly once for this collection.
func (c *Collector) finalize(snapshot *Snapshot, status Status, msg string, notify bool) {
	entry := c.updateStatus(snapshot, status, msg)

	if notify && c.hook != nil {
		c.hook(entry)
	}
}

func (c *Collector) runProcessor(snapshot *Snapshot, notify bool) error {
	c.updateStatus(snapshot, StatusPending, "Processing")

	r, err := c.processor.Process(snapshot)
	if err != nil {
		go snapshot.Prune()
		c.finalize(snapshot, StatusFail, err.Error(), notify)
		return fmt.Errorf("processor aborted: %w", err)
	}
	if r != nil {
		r.Close()
	}

	c.finalize(snapshot, StatusOk, "Ready", notify)
	return nil
}

func (c *Collector) Get(id string) (io.ReadCloser, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ent, ok := c.data[id]
	if !ok {
		return nil, fmt.Errorf("no such entry: %v", ent)
	}

	return c.processor.Process(ent.Snapshot)
}

func (c *Collector) List() []*Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	resp := make([]*Entry, 0, len(c.data))
	for _, ent := range c.data {
		resp = append(resp, ent)
	}
	return resp
}

func (c *Collector) Collect(target *SnapshotTarget) error {
	if target.URL == "" || target.Duration == 0 {
		return fmt.Errorf("URL and Duration cannot be nil")
	}

	snapshot := newSnapshot(c.store, c.typ, c.ext, target)
	c.updateStatus(snapshot, StatusPending, "Collecting")

	if err := snapshot.Collect(); err != nil {
		c.finalize(snapshot, StatusFail, err.Error(), true)
		return fmt.Errorf("failed to collect: %w", err)
	}

	if err := c.runProcessor(snapshot, true); err != nil {
		return fmt.Errorf("failed to process: %w", err)
	}
	return nil
}

func (c *Collector) Add(target *SnapshotTarget, content []byte) (*Snapshot, error) {
	snapshot := newSnapshot(c.store, c.typ, c.ext, target)
	c.updateStatus(snapshot, StatusPending, "Collecting")

	if err := snapshot.Add(content); err != nil {
		c.finalize(snapshot, StatusFail, err.Error(), true)
		return nil, fmt.Errorf("failed to collect: %w", err)
	}

	if err := c.runProcessor(snapshot, true); err != nil {
		return nil, fmt.Errorf("failed to process: %w", err)
	}
	return snapshot, nil
}
