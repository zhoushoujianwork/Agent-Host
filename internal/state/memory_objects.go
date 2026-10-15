package state

import (
	"bytes"
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
		key := annotation.IdentityKey(o.Kind, o.Name)
		if !boundedExternalText(o.Name, 128) || o.Name != strings.TrimSpace(o.Name) || strings.ContainsAny(o.Name, "\r\n\t") || seen[key] || !validObjectKind(o.Kind) {
			return errors.New("objects require unique bounded names and a supported entity kind")
		}
		seen[key] = true
		e := o.Evidence
		containsName := func(text string) bool { return strings.Contains(strings.ToLower(text), strings.ToLower(o.Name)) }
		grounded := containsName(e.Text) || (o.Kind == "person" && strings.EqualFold(o.Name, e.Author) && e.Role == "user")
		if e.Role == "tool" && e.Tool != nil {
			grounded = grounded || containsName(e.Tool.Name) || containsName(string(e.Tool.Input)) || containsName(string(e.Tool.Result))
		}
		toolValid := e.Role == "tool" && e.Tool != nil && boundedExternalText(e.Tool.Name, 128) && json.Valid(e.Tool.Input) && json.Valid(e.Tool.Result) && e.Tool.IsError != nil
		if (e.Role != "user" || e.Tool != nil) && !toolValid || !boundedExternalText(e.ID, 128) || !boundedExternalText(e.Author, 256) || !boundedExternalText(e.Text, 8192) || e.ObservedAt.IsZero() || !grounded || !boundedExternalText(o.Source.Entry, 64) || o.Source.ObservedAt.IsZero() || len(o.Source.Conversation) > 512 || len(o.Source.Turn) > 512 {
			return errors.New("object context requires attributed user/tool evidence grounding its name or authenticated author")
		}
	}
	return nil
}

func validObjectKind(kind string) bool {
	switch kind {
	case "system", "service", "project", "tool", "person", "organization":
		return true
	}
	return false
}

func ValidateMemoryObjects(objects []MemoryObject) error { return validateMemoryObjects(objects) }

// AssociateMemoryObjects changes context metadata through normal publication;
// it cannot enable disabled memories, rewrite their statements or change scope.
func (s *Store) AssociateMemoryObjects(ctx context.Context, scope, id string, revision int, objects []MemoryObject, extractionAudit ...json.RawMessage) (Memory, error) {
	var result Memory
	if len(extractionAudit) > 1 || len(extractionAudit) == 1 && !json.Valid(extractionAudit[0]) {
		return result, errors.New("one valid object extraction audit is required")
	}
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
		if reflect.DeepEqual(m.Objects, objects) && len(m.ObjectAudit) > 0 && (len(extractionAudit) == 0 || bytes.Equal(m.ObjectAudit, extractionAudit[0])) {
			result = m
			return nil
		}
		m.Objects = append([]MemoryObject(nil), objects...)
		m.ObjectAudit = json.RawMessage(`{"policy":"explicit-object-association-v1","status":"identified"}`)
		if len(extractionAudit) == 1 {
			m.ObjectAudit = extractionAudit[0]
		}
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
	left, right := map[string]bool{}, map[string]bool{}
	for _, o := range a {
		left[annotation.IdentityKey(o.Kind, o.Name)] = true
	}
	for _, o := range b {
		right[annotation.IdentityKey(o.Kind, o.Name)] = true
	}
	if len(left) != len(right) {
		return false
	}
	for key := range right {
		if !left[key] {
			return false
		}
	}
	return true
}
