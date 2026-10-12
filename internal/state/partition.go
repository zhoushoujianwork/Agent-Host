package state

import (
	"context"
	"errors"
)

const DefaultAgentID = "default"
const OwnerAudienceID = "owner"

// WithPartition shares the connection and its lock, but scopes native state and
// learned knowledge to an agent and audience. Projects and the channel queue
// remain host-wide. Closing a borrowed handle does not close the owning store.
func (s *Store) WithPartition(agentID, audienceID string) *Store {
	next := *s
	next.agentID, next.audienceID = normalizedPartition(agentID, audienceID)
	next.borrowed = true
	return &next
}

func normalizedPartition(agentID, audienceID string) (string, string) {
	if agentID == "" {
		agentID = DefaultAgentID
	}
	if audienceID == "" {
		audienceID = OwnerAudienceID
	}
	return agentID, audienceID
}

func (s *Store) Partition() (string, string) { return normalizedPartition(s.agentID, s.audienceID) }

func (s *Store) matchesPartition(agentID, audienceID string) bool {
	a, b := s.Partition()
	c, d := normalizedPartition(agentID, audienceID)
	return a == c && b == d
}

func samePartition(a, b, c, d string) bool {
	a, b = normalizedPartition(a, b)
	c, d = normalizedPartition(c, d)
	return a == c && b == d
}

func (s *Store) assignPartition(agentID, audienceID *string) error {
	a, b := s.Partition()
	if *agentID != "" && *agentID != a || *audienceID != "" && *audienceID != b {
		return errors.New("record belongs to another agent or audience")
	}
	*agentID, *audienceID = a, b
	return nil
}

func (s *Store) readRun(ctx context.Context, q rowQuerier, id string) (Run, error) {
	r, err := readRun(ctx, q, id)
	if err == nil && !s.matchesPartition(r.AgentID, r.AudienceID) {
		return Run{}, errors.New("run belongs to another agent or audience")
	}
	return r, err
}
func (s *Store) readSession(ctx context.Context, q rowQuerier, id string) (Session, error) {
	r, err := readSession(ctx, q, id)
	if err == nil && !s.matchesPartition(r.AgentID, r.AudienceID) {
		return Session{}, errors.New("session belongs to another agent or audience")
	}
	return r, err
}
func (s *Store) readMemory(ctx context.Context, q rowQuerier, id string) (Memory, error) {
	r, err := readMemory(ctx, q, id)
	if err == nil && !s.matchesPartition(r.AgentID, r.AudienceID) {
		return Memory{}, errors.New("memory belongs to another agent or audience")
	}
	return r, err
}
func (s *Store) readCandidate(ctx context.Context, q rowQuerier, id string) (Candidate, error) {
	r, err := readCandidate(ctx, q, id)
	if err == nil && !s.matchesPartition(r.AgentID, r.AudienceID) {
		return Candidate{}, errors.New("candidate belongs to another agent or audience")
	}
	return r, err
}

// A column predicate is applied before lookup limits, alias resolution and FTS.
func partitionWhere(alias string, partition ...string) (string, []any) {
	if len(partition) == 0 {
		// Historical annotation migrations precede ownership columns.
		return "", nil
	}
	a, b := normalizedPartition(partition[0], partition[1])
	return " AND " + alias + "agent_id=? AND " + alias + "audience_id=?", []any{a, b}
}

// Keep raw legacy payloads byte-for-byte. Only the rebuilt table's ownership
// columns receive defaults; IDs and all referencing evidence remain unchanged.
const schemaPartitionsV10 = `
CREATE TABLE memories_v10(id TEXT PRIMARY KEY, agent_id TEXT NOT NULL DEFAULT 'default', audience_id TEXT NOT NULL DEFAULT 'owner', sender_id TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL, text TEXT NOT NULL, status TEXT NOT NULL, payload BLOB NOT NULL, expires_at TEXT, UNIQUE(agent_id,audience_id,scope,sender_id,text));
INSERT INTO memories_v10(rowid,id,scope,text,status,payload,expires_at) SELECT rowid,id,scope,text,status,payload,expires_at FROM memories;
DROP TABLE memories;
ALTER TABLE memories_v10 RENAME TO memories;
CREATE INDEX memories_scope ON memories(agent_id,audience_id,scope,status);
ALTER TABLE candidates ADD COLUMN agent_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE candidates ADD COLUMN audience_id TEXT NOT NULL DEFAULT 'owner';
DROP INDEX candidates_scope;
CREATE INDEX candidates_scope ON candidates(agent_id,audience_id,scope,run_id,status);
PRAGMA user_version=10;
`

func conversationType(value string) string {
	if value == "" {
		return "1"
	}
	return value
}
