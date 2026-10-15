package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"agent-host/internal/annotation"
	"agent-host/internal/decision/jev"
	"agent-host/internal/host"
	"agent-host/internal/state"
)

type externalMemoryRequest struct {
	Action     string                    `json:"action"`
	Connection string                    `json:"connection"`
	Agent      string                    `json:"agent,omitempty"`
	Project    string                    `json:"project,omitempty"`
	Receipt    string                    `json:"receipt,omitempty"`
	Key        string                    `json:"key,omitempty"`
	Query      string                    `json:"query,omitempty"`
	Memory     string                    `json:"memory,omitempty"`
	Revision   int                       `json:"revision,omitempty"`
	Submission *state.ExternalSubmission `json:"submission,omitempty"`
	Objects    []state.MemoryObject      `json:"objects,omitempty"`
}

type externalMemoryControl struct {
	store     *state.Store
	resolve   func(context.Context, string, string) (state.ExternalConnection, error)
	processor func(state.ExternalConnection) *host.Host
	wake      chan struct{}
}

func externalReceiptView(r state.ExternalReceipt) any {
	results := append([]state.ExternalItemResult(nil), r.Results...)
	for i := range results {
		results[i].Audit = nil
	}
	return map[string]any{"id": r.ID, "connection": r.Connection, "idempotency_key": r.Submission.Key,
		"digest": r.Digest, "received_at": r.ReceivedAt, "ingestion_status": r.IngestionStatus,
		"processing_status": r.ProcessingStatus, "results": results}
}

func newExternalMemoryControl(s *state.Store, o options, client *jev.Client) *externalMemoryControl {
	x := &externalMemoryControl{store: s, wake: make(chan struct{}, 1)}
	x.resolve = func(ctx context.Context, agent, project string) (state.ExternalConnection, error) {
		catalog, err := loadAgents(o.agentsConfig)
		if err != nil {
			return state.ExternalConnection{}, err
		}
		if agent == "" {
			agent = catalog.DefaultAgent
		}
		a, err := catalog.definition(agent)
		if err != nil {
			return state.ExternalConnection{}, err
		}
		c := state.ExternalConnection{AgentID: agent, AudienceID: state.OwnerAudienceID, ProjectID: project, Enabled: true}
		if project != "" {
			projects, err := a.projects(ctx, s)
			if err != nil {
				return c, err
			}
			for _, p := range projects {
				if p.ID == project {
					c.Scope = p.Scope
					return c, nil
				}
			}
			return c, errors.New("project is unavailable to this configured Agent")
		}
		c.Scope = o.defaultScope
		if c.Scope == "" {
			c.Scope = o.scope
		}
		if c.Scope == "" {
			c.Scope = a.Workspace
		}
		if c.Scope == "" {
			c.Scope = o.workspace
		}
		if c.Scope == "" {
			return c, errors.New("Host default memory scope is unavailable")
		}
		return c, nil
	}
	x.processor = func(c state.ExternalConnection) *host.Host {
		selected := o
		selected.agentID, selected.audienceID, selected.scope = c.AgentID, c.AudienceID, c.Scope
		return composeHost(s, selected, client, nil)
	}
	return x
}

func (x *externalMemoryControl) binding(ctx context.Context, id string) (state.ExternalConnection, error) {
	c, err := x.store.ExternalConnection(ctx, id)
	if err != nil {
		return c, err
	}
	current, err := x.resolve(ctx, c.AgentID, c.ProjectID)
	if err != nil {
		return c, err
	}
	current.ID = c.ID
	if current != c {
		return c, errors.New("memory connection no longer matches Host configuration; create a new connection")
	}
	return c, nil
}

func (x *externalMemoryControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, err error) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	if r.Method != http.MethodPost {
		fail(405, errors.New("external memory requires POST"))
		return
	}
	var a externalMemoryRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, state.MaxExternalBytes+4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil || d.Decode(new(any)) != io.EOF {
		fail(400, errors.New("invalid bounded external memory request"))
		return
	}
	if !agentIdentifier.MatchString(a.Connection) || len(a.Agent) > 64 || len(a.Project) > 128 || len(a.Receipt) > 128 || len(a.Key) > 128 || len(a.Memory) > 128 || len(a.Query) > 256 {
		fail(400, errors.New("invalid external memory selectors"))
		return
	}
	if !x.store.FileMemoryEnabled() {
		fail(409, errors.New("external memory requires explicit file-memory migration"))
		return
	}
	timeout := externalMemoryTimeout
	if a.Action == "extract-objects" {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var value any
	var err error
	if a.Action == "connection-add" {
		var c state.ExternalConnection
		c, err = x.resolve(ctx, a.Agent, a.Project)
		c.ID = a.Connection
		if err == nil {
			err = x.store.SaveExternalConnection(ctx, c)
		}
		value = c
	} else {
		var c state.ExternalConnection
		c, err = x.binding(ctx, a.Connection)
		if err == nil {
			s := x.store.WithPartition(c.AgentID, c.AudienceID)
			switch a.Action {
			case "connection-show":
				value = c
			case "connection-revoke":
				c.Enabled = false
				err = x.store.SaveExternalConnection(ctx, c)
				value = c
			case "submit":
				if a.Submission == nil {
					err = errors.New("submission is required")
					break
				}
				var receipt state.ExternalReceipt
				receipt, err = x.store.ReceiveExternal(ctx, c.ID, host.ID(), *a.Submission)
				value = externalReceiptView(receipt)
				if err == nil {
					select {
					case x.wake <- struct{}{}:
					default:
					}
				}
			case "receipt":
				var receipt state.ExternalReceipt
				receipt, err = x.store.ExternalReceipt(ctx, c.ID, a.Receipt, a.Key)
				value = externalReceiptView(receipt)
			case "associate":
				value, err = s.AssociateMemoryObjects(ctx, c.Scope, a.Memory, a.Revision, a.Objects)
			case "extract-objects":
				value, err = x.processor(c).ExtractMemoryObjects(ctx, c.Scope, a.Memory, a.Revision)
			case "recall":
				if a.Query == "" {
					err = errors.New("recall requires a query")
					break
				}
				var result state.Retrieval
				result, err = s.Retrieve(ctx, c.Scope, a.Query, 10)
				if err == nil {
					// Search is navigation; full source material is read explicitly.
					items := []map[string]any{}
					for _, m := range result.Matches {
						text := []rune(m.Text)
						if len(text) > 160 {
							text = text[:160]
						}
						objects := []map[string]string{}
						for _, o := range m.Objects {
							objects = append(objects, map[string]string{"id": o.ID(m.Scope), "name": annotation.DisplayName(o.Kind, o.Name), "kind": o.Kind})
						}
						items = append(items, map[string]any{"id": m.ID, "revision": m.Revision, "kind": m.Kind, "snippet": string(text), "external_receipt_id": m.ExternalReceiptID, "objects": objects})
					}
					value = map[string]any{"items": items, "connection": c, "bounded": true, "query_analysis": result.Query}
				}
			case "read":
				var m state.Memory
				m, err = s.ContextMemory(ctx, c.Scope, a.Memory, a.Revision)
				if err == nil {
					var evidence any
					evidence, err = s.ContextMemoryEvidence(ctx, c.Scope, m.ID, m.Revision)
					m.RevisionDecision = nil
					value = map[string]any{"memory": m, "supporting_evidence": evidence}
				}
			default:
				err = errors.New("unknown external memory operation")
			}
		}
	}
	if err != nil {
		fail(409, err)
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func (x *externalMemoryControl) start(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			jobs, err := x.store.PendingExternal(ctx)
			if err != nil && ctx.Err() == nil {
				log.Printf("external memory queue unavailable: %v", err)
			}
			if err == nil {
				for _, r := range jobs {
					if ctx.Err() != nil {
						return
					}
					c, err := x.binding(ctx, r.Connection.ID)
					if err != nil {
						continue
					}
					jobCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
					if err := x.processor(c).ProcessExternal(jobCtx, r); err != nil && ctx.Err() == nil {
						log.Printf("external memory receipt %s remains unfinished: %v", r.ID, err)
					}
					stop()
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-x.wake:
			}
		}
	}()
	return func() { cancel(); <-done }
}
