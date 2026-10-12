package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agent-host/internal/annotation"
)

// Retrieval keeps candidate evidence separate from final context assembly.
type Retrieval struct {
	Query      annotation.Query    `json:"query"`
	Matches    []Memory            `json:"matches"`
	Persistent []Memory            `json:"persistent"`
	LexicalIDs []string            `json:"lexical_ids"`
	Decisions  []RetrievalDecision `json:"decisions"`
}

type RetrievalDecision struct {
	MemoryID string   `json:"memory_id"`
	Accepted bool     `json:"accepted"`
	Reason   string   `json:"reason"`
	Routes   []string `json:"routes"`
}

type annotatedMemory struct {
	Memory     Memory
	Annotation annotation.Annotation
}

func rawAnnotation(ctx context.Context, tx *sql.Tx, m Memory) error {
	a := annotation.Extract(m.Scope, m.Text)
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_annotations(memory_id,version,payload) VALUES(?,?,?) ON CONFLICT(memory_id) DO UPDATE SET version=excluded.version,payload=excluded.payload`, m.ID, annotation.Version, payload)
	return wrap("persist memory annotation", err)
}

// Derived rows can be rebuilt without changing source text, review provenance,
// historical revisions, or execution evidence.
func backfillAnnotations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM memories ORDER BY rowid")
	if err != nil {
		return err
	}
	memories, scanErr := scanMemories(rows)
	if err = errors.Join(scanErr, rows.Close()); err != nil {
		return err
	}
	scopes := map[string]bool{}
	for _, m := range memories {
		if err = rawAnnotation(ctx, tx, m); err != nil {
			return err
		}
		scopes[m.Scope] = true
	}
	for scope := range scopes {
		if err = rebuildTerms(ctx, tx, scope); err != nil {
			return err
		}
	}
	return nil
}

func persistAnnotation(ctx context.Context, tx *sql.Tx, m Memory) error {
	if err := rawAnnotation(ctx, tx, m); err != nil {
		return err
	}
	// Adding or withdrawing an explicit alias affects other memories in the
	// scope. Rebuild their derived keys atomically with the lifecycle change.
	return rebuildTerms(ctx, tx, m.Scope, m.AgentID, m.AudienceID)
}

func eligibleAnnotations(ctx context.Context, tx *sql.Tx, scope string, now time.Time, partition ...string) ([]annotatedMemory, error) {
	filter, args := partitionWhere("m.", partition...)
	args = append([]any{scope, timestamp(now)}, args...)
	rows, err := tx.QueryContext(ctx, `SELECT m.payload,a.version,a.payload FROM memories m LEFT JOIN memory_annotations a ON a.memory_id=m.id WHERE m.scope=? AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>?)`+filter+` ORDER BY m.rowid LIMIT 201`, args...)
	if err != nil {
		return nil, wrap("read eligible annotations", err)
	}
	defer rows.Close()
	result := []annotatedMemory{}
	for rows.Next() {
		var memoryPayload, annotationPayload []byte
		var version sql.NullString
		if err = rows.Scan(&memoryPayload, &version, &annotationPayload); err != nil {
			return nil, err
		}
		var item annotatedMemory
		if err = json.Unmarshal(memoryPayload, &item.Memory); err != nil {
			return nil, err
		}
		item.Memory.AgentID, item.Memory.AudienceID = normalizedPartition(item.Memory.AgentID, item.Memory.AudienceID)
		if !version.Valid || version.String != annotation.Version {
			return nil, fmt.Errorf("memory %s requires annotation migration to %s", item.Memory.ID, annotation.Version)
		}
		if err = json.Unmarshal(annotationPayload, &item.Annotation); err != nil {
			return nil, fmt.Errorf("read annotation for memory %s: %w", item.Memory.ID, err)
		}
		if item.Annotation.Version != annotation.Version {
			return nil, fmt.Errorf("memory %s has inconsistent annotation version", item.Memory.ID)
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > 200 {
		return nil, errors.New("scope exceeds MVP annotation limit of 200 eligible memories; narrower registry retrieval is required")
	}
	return result, nil
}

func annotationRecords(items []annotatedMemory) []annotation.Annotation {
	result := make([]annotation.Annotation, 0, len(items))
	for _, item := range items {
		result = append(result, item.Annotation)
	}
	return result
}

func rebuildTerms(ctx context.Context, tx *sql.Tx, scope string, partition ...string) error {
	items, err := eligibleAnnotations(ctx, tx, scope, time.Now(), partition...)
	if err != nil {
		return err
	}
	filter, args := partitionWhere("", partition...)
	args = append([]any{scope}, args...)
	if _, err = tx.ExecContext(ctx, "DELETE FROM memory_terms WHERE memory_id IN (SELECT id FROM memories WHERE scope=?"+filter+")", args...); err != nil {
		return err
	}
	records := annotationRecords(items)
	for _, item := range items {
		query := annotation.Resolve(scope, item.Memory.Text, records)
		for _, key := range annotation.QueryKeys(query) {
			if _, err = tx.ExecContext(ctx, "INSERT INTO memory_terms(memory_id,term) VALUES(?,?) ON CONFLICT DO NOTHING", item.Memory.ID, key); err != nil {
				return wrap("index memory annotation", err)
			}
		}
	}
	return nil
}

// Retrieve uses one SQLite snapshot for the alias registry, indexed candidates,
// and lexical candidates. It checks applicability before imposing the limit.
func (s *Store) Retrieve(ctx context.Context, scope, text string, limit int) (result Retrieval, err error) {
	result.Matches = []Memory{}
	result.Persistent = []Memory{}
	result.LexicalIDs = []string{}
	result.Decisions = []RetrievalDecision{}
	if scope == "" {
		return result, errors.New("search scope is required")
	}
	if limit < 1 || limit > 200 {
		return result, errors.New("search limit must be between 1 and 200")
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		agentID, audienceID := s.Partition()
		items, err := eligibleAnnotations(ctx, tx, scope, now, agentID, audienceID)
		if err != nil {
			return err
		}
		records := annotationRecords(items)
		result.Query = annotation.Resolve(scope, text, records)
		byID := map[string]annotatedMemory{}
		for _, item := range items {
			byID[item.Memory.ID] = item
		}
		routes := map[string]map[string]bool{}
		add := func(id, route string) {
			if routes[id] == nil {
				routes[id] = map[string]bool{}
			}
			routes[id][route] = true
		}
		keys := annotation.QueryKeys(result.Query)
		if len(keys) > 0 {
			args := []any{scope, timestamp(now), agentID, audienceID}
			for _, key := range keys {
				args = append(args, key)
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
			rows, err := tx.QueryContext(ctx, `SELECT t.memory_id,t.term FROM memory_terms t JOIN memories m ON m.id=t.memory_id WHERE m.scope=? AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>?) AND m.agent_id=? AND m.audience_id=? AND t.term IN (`+placeholders+`) ORDER BY t.memory_id,t.term`, args...)
			if err != nil {
				return wrap("search memory annotation index", err)
			}
			for rows.Next() {
				var id, key string
				if err = rows.Scan(&id, &key); err != nil {
					rows.Close()
					return err
				}
				route := "topic"
				if strings.HasPrefix(key, "object:") {
					route = "object"
				}
				add(id, route)
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
		}
		lexicalRank := map[string]int{}
		lexicalIDs, err := indexedLexicalIDs(ctx, tx, scope, searchTokens(text), now, agentID, audienceID)
		if err != nil {
			return err
		}
		for _, id := range lexicalIDs {
			lexicalRank[id] = len(result.LexicalIDs)
			result.LexicalIDs = append(result.LexicalIDs, id)
			add(id, "lexical")
		}
		// Expiration can resolve an alias collision without a write/reindex.
		// Resolve current alias references in the already bounded registry;
		// this also handles one-character names that CJK FTS cannot tokenize.
		resolved := map[string]bool{}
		for _, id := range result.Query.ResolvedIDs {
			resolved[id] = true
		}
		canonical := map[string]annotation.Annotation{}
		memoryQueries := map[string]annotation.Query{}
		for _, item := range items {
			memoryQuery := annotation.Resolve(scope, item.Memory.Text, records)
			memoryQueries[item.Memory.ID] = memoryQuery
			current := annotation.Canonical(memoryQuery)
			canonical[item.Memory.ID] = current
			for _, object := range current.Objects {
				if !resolved[object.ID] {
					continue
				}
				originalIdentity := false
				for _, original := range item.Annotation.Objects {
					if original.ID == object.ID && original.Span.Start == object.Span.Start {
						originalIdentity = true
					}
				}
				if !originalIdentity {
					add(item.Memory.ID, "alias")
				}
			}
		}

		ids := make([]string, 0, len(routes))
		for id := range routes {
			if _, exists := byID[id]; !exists {
				return fmt.Errorf("candidate %s is absent from the eligible annotation registry", id)
			}
			ids = append(ids, id)
		}
		rank := func(id string) int {
			rank := 0
			if routes[id]["object"] {
				rank += 4
			}
			if routes[id]["topic"] {
				rank += 2
			}
			if routes[id]["lexical"] || routes[id]["alias"] {
				rank++
			}
			return rank
		}
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			if rank(a) != rank(b) {
				return rank(a) > rank(b)
			}
			aLexical, bLexical := len(result.LexicalIDs), len(result.LexicalIDs)
			if rank, ok := lexicalRank[a]; ok {
				aLexical = rank
			}
			if rank, ok := lexicalRank[b]; ok {
				bLexical = rank
			}
			if aLexical != bLexical {
				return aLexical < bLexical
			}
			return a < b
		})
		for _, id := range ids {
			item := byID[id]
			accepted, reason := annotation.Compatible(result.Query, canonical[id])
			if len(memoryQueries[id].Ambiguous) > 0 || len(memoryQueries[id].Unknown) > 0 {
				accepted, reason = false, "unresolved_memory_object"
			}
			if accepted && len(result.Matches) >= limit {
				accepted, reason = false, "candidate_limit"
			}
			decision := RetrievalDecision{MemoryID: id, Accepted: accepted, Reason: reason, Routes: []string{}}
			for _, route := range []string{"object", "topic", "lexical", "alias"} {
				if routes[id][route] {
					decision.Routes = append(decision.Routes, route)
				}
			}
			result.Decisions = append(result.Decisions, decision)
			if accepted {
				result.Matches = append(result.Matches, item.Memory)
			}
		}
		// Persistent preferences and constraints pass the same applicability
		// boundary. Only entirely unqualified rules can bypass candidate lookup.
		// This list is independent of the search limit and is budgeted by assembly.
		decisionPositions := map[string]int{}
		for i, decision := range result.Decisions {
			decisionPositions[decision.MemoryID] = i
		}
		for _, item := range items {
			if item.Memory.Kind != "preference" && item.Memory.Kind != "constraint" {
				continue
			}
			id := item.Memory.ID
			memoryQuery := memoryQueries[id]
			generic := len(item.Annotation.Objects) == 0 && len(item.Annotation.Topics) == 0 && len(item.Annotation.Aliases) == 0 && len(canonical[id].Objects) == 0 && len(memoryQuery.Ambiguous) == 0 && len(memoryQuery.Unknown) == 0
			accepted, reason := true, "generic_persistent"
			if !generic {
				accepted, reason = annotation.Compatible(result.Query, canonical[id])
				if len(memoryQuery.Ambiguous) > 0 || len(memoryQuery.Unknown) > 0 {
					accepted, reason = false, "unresolved_persistent_object"
				}
				if accepted && len(routes[id]) == 0 {
					accepted, reason = false, "unmatched_persistent_memory"
				}
				if accepted {
					reason = "compatible_persistent"
				}
			}
			if accepted {
				result.Persistent = append(result.Persistent, item.Memory)
			}
			if i, exists := decisionPositions[id]; exists {
				result.Decisions[i].Routes = append(result.Decisions[i].Routes, "persistent")
				result.Decisions[i].Accepted = result.Decisions[i].Accepted || accepted
				result.Decisions[i].Reason += "; " + reason
			} else {
				result.Decisions = append(result.Decisions, RetrievalDecision{MemoryID: id, Accepted: accepted, Reason: reason, Routes: []string{"persistent"}})
			}
		}
		return nil
	})
	return result, err
}

// Lexical candidates come from genuine FTS queries. SQLite errors stop the
// operation even when other candidate routes succeeded.
func indexedLexicalIDs(ctx context.Context, tx *sql.Tx, scope string, tokens []string, now time.Time, agentID, audienceID string) ([]string, error) {
	ids := []string{}
	if len(tokens) == 0 {
		return ids, nil
	}
	clauses := make([]string, len(tokens))
	for i, token := range tokens {
		clauses[i] = "\"" + token + "\""
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id FROM memory_fts JOIN memories m ON m.id=memory_fts.memory_id WHERE memory_fts MATCH ? AND m.scope=? AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>?) AND m.agent_id=? AND m.audience_id=? ORDER BY bm25(memory_fts),m.id`, strings.Join(clauses, " OR "), scope, timestamp(now), agentID, audienceID)
	if err != nil {
		return nil, wrap("search memory FTS", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
