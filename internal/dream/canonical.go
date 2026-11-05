package dream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"agent-host/internal/event"
	"agent-host/internal/experience"
	"agent-host/internal/harness"
	"agent-host/internal/state"
)

const (
	maxCanonicalEvents        = 512
	maxCanonicalRequest       = 64 << 10
	maxCanonicalArtifact      = 64 << 10
	maxCanonicalTools         = 128 << 10
	maxCanonicalToolPairs     = 256
	maxCanonicalSkillEvidence = 1024
)

type canonicalSource struct {
	Work            state.DreamWorkItem
	Input           ExtractionInput
	AllowedRefs     map[string]bool
	FailedRefs      map[string]bool
	LegacyReceiptID string
}

type canonicalDigest struct {
	RunID, SessionID, NativeSessionID                    string
	ExecutionStatus, AcceptanceStatus, MemoryAttribution string
	RequestedModel, Model, InputDigest, OutputDigest     string
	Error                                                string
	CompletedAt                                          any
	MissingReasons                                       []string
	Events                                               []canonicalEvent
	Tools                                                []canonicalTool
}

type canonicalEvent struct {
	Seq                        int64
	Type, Phase, Status, Model string
	DataDigest                 string
}

type canonicalTool struct {
	ID, Name, InputDigest, ResultDigest string
	IsError                             bool
	CallSeq, ResultSeq                  int64
}

func canonicalRunSource(record state.DreamCanonicalRun, p state.DreamPartition, evidence []state.ToolEvidence, evidenceErr error) (canonicalSource, error) {
	run := record.Run
	toolWindowIncomplete := len(evidence) > maxCanonicalToolPairs
	if toolWindowIncomplete {
		evidence = evidence[len(evidence)-maxCanonicalToolPairs:]
	}
	events := run.Events
	if len(events) > maxCanonicalEvents {
		events = events[len(events)-maxCanonicalEvents:]
	}
	relevant := make([]canonicalEvent, 0, len(events))
	skills := make([]state.DreamSkillEvidence, 0)
	skillWindowIncomplete := false
	allowed, failed := map[string]bool{
		"artifact:canonical-outcome.json":        true,
		"artifact:canonical-events.json":         true,
		"artifact:canonical-skill-evidence.json": true,
		"artifact:host-tool-evidence.json":       true,
		"artifact:canonical-request.txt":         true,
	}, map[string]bool{}
	for _, e := range events {
		if e.Type != "workspace.assembled" && e.Type != "skills.catalog.provided" && e.Type != "skills.load.observed" && e.Type != "skills.reference.observed" && e.Type != "tool.activity" && e.Type != "agent.result" && e.Type != "run.finished" {
			continue
		}
		eventRaw, _ := json.Marshal(e.Data)
		relevant = append(relevant, canonicalEvent{e.Seq, e.Type, e.Phase, e.Status, e.Model, hashBytes(eventRaw)})
		ref := fmt.Sprintf("event:%s:%d", run.ID, e.Seq)
		allowed[ref] = true
		if e.Status == "failed" {
			failed[ref] = true
		}
		observedSkills := skillEvidenceFromEvent(e)
		remaining := maxCanonicalSkillEvidence - len(skills)
		if remaining > 0 {
			if len(observedSkills) > remaining {
				skillWindowIncomplete = true
				observedSkills = observedSkills[:remaining]
			}
			skills = append(skills, observedSkills...)
		} else if len(observedSkills) > 0 {
			skillWindowIncomplete = true
		}
	}
	for _, tool := range evidence {
		ref := "tool:" + tool.ID
		allowed[ref] = true
		if tool.IsError {
			failed[ref] = true
		}
	}
	offered := map[string]state.DreamSkillEvidence{}
	for _, skill := range skills {
		if skill.Kind == "offered" {
			offered[skill.Name] = skill
		}
	}
	for i := range skills {
		if skills[i].Kind != "reference_read" || skills[i].SkillID != "" {
			continue
		}
		for name, source := range offered {
			if strings.Contains("/"+skills[i].Reference, "/skills/"+name+"/") {
				skills[i].SkillID, skills[i].Name, skills[i].Revision, skills[i].Digest = source.SkillID, source.Name, source.Revision, source.Digest
				break
			}
		}
	}
	missing := canonicalMissingReasons(run, len(run.Events) > maxCanonicalEvents, evidenceErr)
	if toolWindowIncomplete {
		missing = append(missing, "tool_evidence_window_incomplete")
	}
	if skillWindowIncomplete {
		missing = append(missing, "skill_evidence_window_incomplete")
	}
	toolDigests := make([]canonicalTool, 0, len(evidence))
	for _, tool := range evidence {
		toolDigests = append(toolDigests, canonicalTool{tool.ID, tool.Name, hashBytes(tool.Input), hashBytes(tool.Result), tool.IsError, tool.CallSeq, tool.ResultSeq})
	}
	digestValue := canonicalDigest{RunID: run.ID, SessionID: run.SessionID, NativeSessionID: run.NativeSessionID, ExecutionStatus: run.ExecutionStatus, AcceptanceStatus: run.AcceptanceStatus, MemoryAttribution: run.MemoryAttribution, RequestedModel: run.RequestedModel, Model: run.Model, InputDigest: hashBytes([]byte(run.Input)), OutputDigest: hashBytes([]byte(run.Output)), Error: run.Error, CompletedAt: run.CompletedAt, MissingReasons: missing, Events: relevant, Tools: toolDigests}
	raw, err := json.Marshal(digestValue)
	if err != nil {
		return canonicalSource{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	sourceKey := "host-run:" + run.ID
	workID := "dream-work-" + hashStrings(p.String(), sourceKey)[:24]
	output, omittedOutput := canonicalUTF8Excerpt([]byte(run.Output), maxCanonicalArtifact)
	request, omittedRequest := canonicalUTF8Excerpt([]byte(run.Input), maxCanonicalRequest)
	outcome, _ := json.Marshal(map[string]any{
		"run_id": run.ID, "session_id": run.SessionID, "native_session_id": run.NativeSessionID,
		"execution_status": run.ExecutionStatus, "acceptance_status": run.AcceptanceStatus,
		"memory_attribution": run.MemoryAttribution, "requested_model": run.RequestedModel, "model": run.Model,
		"completed_at": run.CompletedAt, "output": output, "output_omitted_bytes": omittedOutput, "error": run.Error,
	})
	toolBody, _ := json.Marshal(experience.LearningEvidence(evidence))
	toolBody = boundedCanonicalBytes(toolBody, maxCanonicalTools)
	skillBody, _ := json.Marshal(skills)
	eventBody, _ := json.Marshal(relevant)
	documents := []Document{
		{Path: "canonical-outcome.json", Body: outcome},
		{Path: "canonical-events.json", Body: eventBody},
		{Path: "canonical-skill-evidence.json", Body: skillBody},
		{Path: "host-tool-evidence.json", Body: toolBody},
		{Path: "canonical-request.txt", Body: []byte(fmt.Sprintf("[omitted_bytes=%d]\n%s", omittedRequest, request))},
	}
	work := state.DreamWorkItem{ID: workID, Partition: p, SourceKey: sourceKey, Source: sourceKey, RunID: run.ID, SessionID: run.SessionID, EvidenceDigest: digest, PolicyVersion: state.DreamWorkPolicyVersion, MissingReasons: missing, SkillEvidence: skills}
	input := ExtractionInput{Partition: p, Source: sourceKey, Directory: sourceKey, Digest: digest, Attribution: "host_observed", CanonicalRunID: run.ID, CanonicalSessionID: run.SessionID, Documents: documents}
	return canonicalSource{Work: work, Input: input, AllowedRefs: allowed, FailedRefs: failed}, nil
}

func hashBytes(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func boundedCanonicalBytes(body []byte, limit int) []byte {
	text, omitted := canonicalUTF8Excerpt(body, limit)
	if omitted == 0 {
		return []byte(text)
	}
	return []byte(fmt.Sprintf("{\"coverage\":\"truncated\",\"omitted_bytes\":%d,\"excerpt\":%q}", omitted, text))
}

func canonicalUTF8Excerpt(body []byte, limit int) (string, int) {
	if len(body) <= limit {
		return strings.ToValidUTF8(string(body), "?"), 0
	}
	if limit < 64 {
		return "", len(body)
	}
	head := limit / 2
	tail := len(body) - (limit - head)
	for head > 0 && !utf8.RuneStart(body[head]) {
		head--
	}
	for tail < len(body) && !utf8.RuneStart(body[tail]) {
		tail++
	}
	marker := fmt.Sprintf("\n[... OMITTED %d SOURCE BYTES ...]\n", tail-head)
	for head+len(body)-tail+len(marker) > limit && head > 0 {
		head--
	}
	return strings.ToValidUTF8(string(body[:head]), "?") + marker + strings.ToValidUTF8(string(body[tail:]), "?"), tail - head
}

func canonicalMissingReasons(run state.Run, incompleteEvents bool, evidenceErr error) []string {
	var reasons []string
	if run.ExecutionStatus != "completed" {
		reasons = append(reasons, "execution_not_completed")
	}
	switch run.AcceptanceStatus {
	case "accepted", "succeeded", "completed":
	default:
		reasons = append(reasons, "acceptance_not_positive")
	}
	if run.MemoryAttribution == "unresolved" {
		reasons = append(reasons, "memory_attribution_unresolved")
	}
	if evidenceErr != nil {
		reasons = append(reasons, "tool_evidence_incomplete")
	}
	if incompleteEvents {
		reasons = append(reasons, "event_window_incomplete")
	}
	return reasons
}

func skillEvidenceFromEvent(e event.Event) []state.DreamSkillEvidence {
	if e.Type == "skills.load.observed" || e.Type == "skills.reference.observed" {
		item := state.DreamSkillEvidence{EventSeq: e.Seq, Status: e.Status}
		if e.Type == "skills.load.observed" {
			item.Kind = "loaded"
			decodeSkillSource(e.Data["source"], &item)
		} else {
			item.Kind = "reference_read"
			var file harness.SkillFile
			if remarshal(e.Data["file"], &file) == nil {
				item.Reference, item.ReferenceDigest = file.Path, file.SHA256
			}
		}
		return []state.DreamSkillEvidence{item}
	}
	if e.Type != "workspace.assembled" && e.Type != "skills.catalog.provided" {
		return nil
	}
	var manifest harness.SkillManifest
	value := e.Data["skill_manifest"]
	if e.Type == "skills.catalog.provided" {
		value = e.Data["manifest"]
	}
	if remarshal(value, &manifest) != nil {
		return nil
	}
	items := make([]state.DreamSkillEvidence, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		item := state.DreamSkillEvidence{EventSeq: e.Seq, Kind: "offered", Status: "completed"}
		fillSkillSource(source, &item)
		items = append(items, item)
	}
	return items
}

func decodeSkillSource(value any, item *state.DreamSkillEvidence) {
	var source harness.SkillSource
	if remarshal(value, &source) == nil {
		fillSkillSource(source, item)
	}
}

func fillSkillSource(source harness.SkillSource, item *state.DreamSkillEvidence) {
	item.SkillID, item.Name, item.Revision, item.Digest = source.SkillID, source.Name, source.Revision, source.ContentDigest
	if item.SkillID == "" {
		item.SkillID = source.MemoryID
	}
}

func remarshal(value, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func candidateIDs(items []state.DreamCandidate) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value], out = true, append(out, value)
		}
	}
	return out
}
