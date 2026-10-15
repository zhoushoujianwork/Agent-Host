package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"agent-host/internal/annotation"
)

// MemoryObject is an attributed context assertion, separate from the exact
// memory statement. It grants no permission or independent factual certainty.
type MemoryObject struct {
	Name     string           `json:"name"`
	Kind     string           `json:"kind"`
	Source   ExternalSource   `json:"source"`
	Evidence ExternalEvidence `json:"evidence"`
}

func (o MemoryObject) ID(scope string) string {
	return annotation.ContextObject(scope, o.Kind, o.Name, o.Evidence.ID).ID
}

func validateMemoryObjects(objects []MemoryObject) error {
	if len(objects) > 3 {
		return errors.New("at most three explicit object associations are supported")
	}
	seen := map[string]bool{}
	for _, o := range objects {
		key := o.Kind + ":" + strings.ToLower(o.Name)
		if !boundedExternalText(o.Name, 128) || o.Name != strings.TrimSpace(o.Name) || strings.ContainsAny(o.Name, "\r\n\t") || seen[key] || (o.Kind != "system" && o.Kind != "service" && o.Kind != "project") {
			return errors.New("objects require unique bounded names and system, service or project kind")
		}
		seen[key] = true
		e := o.Evidence
		if e.Role != "user" || e.Tool != nil || !boundedExternalText(e.ID, 128) || !boundedExternalText(e.Author, 256) || !boundedExternalText(e.Text, 8192) || e.ObservedAt.IsZero() || !strings.Contains(e.Text, o.Name) || !boundedExternalText(o.Source.Entry, 64) || o.Source.ObservedAt.IsZero() || len(o.Source.Conversation) > 512 || len(o.Source.Turn) > 512 {
			return errors.New("object context requires an attributed user excerpt containing the exact object name and source observation time")
		}
	}
	return nil
}

// AssociateMemoryObjects changes context metadata through normal publication;
// it cannot enable disabled memories, rewrite their statements or change scope.
func (s *Store) AssociateMemoryObjects(ctx context.Context, scope, id string, revision int, objects []MemoryObject) (Memory, error) {
	var result Memory
	if len(objects) == 0 || revision < 1 {
		return result, errors.New("association requires 1–3 objects and an expected revision")
	}
	if err := validateMemoryObjects(objects); err != nil {
		return result, err
	}
	err := s.memoryTransaction(ctx, func(tx *sql.Tx) error {
		m, err := s.readMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Scope != scope || m.Revision != revision || !recallEligible(m, time.Now()) {
			return errors.New("object association requires the current active memory in this scope")
		}
		if reflect.DeepEqual(m.Objects, objects) {
			result = m
			return nil
		}
		m.Objects = append([]MemoryObject(nil), objects...)
		m.Revision++
		m.RevisionDecision = json.RawMessage(`{"policy":"explicit-object-association-v1"}`)
		if err := persistMemory(ctx, tx, m, false); err != nil {
			return err
		}
		result = m
		return nil
	})
	return result, err
}

func objectAnnotation(m Memory, a annotation.Annotation) annotation.Annotation {
	objects := []annotation.Object{}
	for _, o := range m.Objects {
		objects = append(objects, annotation.ContextObject(m.Scope, o.Kind, o.Name, o.Evidence.ID))
	}
	return annotation.WithContextObjects(a, objects)
}

func sameObjectIdentities(a, b []MemoryObject) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, o := range a {
		set[o.Kind+":"+strings.ToLower(o.Name)] = true
	}
	for _, o := range b {
		if !set[o.Kind+":"+strings.ToLower(o.Name)] {
			return false
		}
	}
	return true
}
