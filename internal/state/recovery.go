package state

import (
	"agent-host/internal/event"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

// MemoryRecovery is a logical checkpoint, not a copy of the operational database.
// Evidence retains its original payload; import separately suppresses resumption.
type MemoryRecovery struct {
	Version     int                  `json:"version"`
	Config      FileMemoryConfig     `json:"config"`
	Generation  uint64               `json:"generation"`
	Memories    []Memory             `json:"memories"`
	Evidence    map[string][]string  `json:"evidence"`
	Runs        []Run                `json:"runs"`
	Sessions    []Session            `json:"sessions"`
	Candidates  []Candidate          `json:"candidates"`
	Receipts    []ExternalReceipt    `json:"receipts"`
	Connections []ExternalConnection `json:"connections"`
	Projects    []Project            `json:"projects"`
	Messages    []ChannelMessage     `json:"messages"`
	Attachments map[string][]byte    `json:"attachments,omitempty"`
}

// CaptureMemoryRecovery takes the publication lock before a SQLite read snapshot.
// It never opens the writable backend or modifies operational data or its index.
func CaptureMemoryRecovery(ctx context.Context, home string) (MemoryRecovery, error) {
	r := MemoryRecovery{Version: 1, Evidence: map[string][]string{}}
	var err error
	home, err = physicalPath(home)
	if err != nil {
		return r, err
	}
	if err = decodePrivateJSON(FileMemoryMarker(home), &r.Config); err != nil {
		return r, err
	}
	if err = validateFileConfig(r.Config, home); err != nil {
		return r, err
	}
	f := &fileMemory{config: r.Config, sourceDir: filepath.Join(r.Config.MemoryRoot, "sources", sourceKey(r.Config.SourceID))}
	release, err := f.lock(ctx)
	if err != nil {
		return r, err
	}
	defer release()
	snap, err := f.readSnapshot()
	if err != nil {
		return r, err
	}
	if len(snap.manifest.Operations) > 0 {
		if _, err = readPrivateFile(filepath.Join(f.sourceDir, "applied", fmt.Sprint(snap.manifest.Generation)+".json")); err != nil {
			return r, errors.New("publication receipt recovery must finish before backup")
		}
	}
	r.Generation, r.Memories, r.Evidence = snap.manifest.Generation, snap.current, snap.evidence
	u := url.URL{Scheme: "file", Path: filepath.Join(home, "state.db")}
	u.RawQuery = "mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return r, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	var schema int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return r, err
	}
	if schema < 14 || schema > SchemaVersion {
		return r, fmt.Errorf("backup supports file-memory state schemas 14–%d, got %d", SchemaVersion, schema)
	}
	runs, sessions, candidates, receipts, messages := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range r.Memories {
		if m.SourceRunID != "" {
			runs[m.SourceRunID] = true
		}
		for _, id := range r.Evidence[m.ID] {
			runs[id] = true
		}
		if m.CandidateID != "" {
			candidates[m.CandidateID] = true
		}
		if m.ExternalReceiptID != "" {
			receipts[m.ExternalReceiptID] = true
		}
	}
	read := func(table, id string, value any) error {
		var b []byte
		if err := tx.QueryRowContext(ctx, "SELECT payload FROM "+table+" WHERE id=?", id).Scan(&b); err != nil {
			return fmt.Errorf("required backup evidence %s/%s: %w", table, id, err)
		}
		return json.Unmarshal(b, value)
	}
	for _, id := range recoveryKeys(candidates) {
		var c Candidate
		if err = read("candidates", id, &c); err != nil {
			return r, err
		}
		r.Candidates = append(r.Candidates, c)
		runs[c.RunID] = true
	}
	for _, id := range recoveryKeys(runs) {
		run, e := readRun(ctx, tx, id)
		if e != nil {
			return r, fmt.Errorf("required backup run %s: %w", id, e)
		}
		rows, e := tx.QueryContext(ctx, "SELECT payload FROM events WHERE run_id=? ORDER BY seq", id)
		if e != nil {
			return r, e
		}
		run.Events = nil
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e == nil {
				var ev event.Event
				e = json.Unmarshal(b, &ev)
				run.Events = append(run.Events, ev)
			}
			if e != nil {
				rows.Close()
				return r, e
			}
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			return r, e
		}
		r.Runs = append(r.Runs, run)
		sessions[run.SessionID] = true
		if run.Source != nil && run.Source.ChannelMessageID != "" {
			messages[run.Source.ChannelMessageID] = true
		}
	}
	for _, id := range recoveryKeys(messages) {
		var message ChannelMessage
		if err = read("channel_messages", id, &message); err != nil {
			return r, err
		}
		r.Messages = append(r.Messages, message)
	}
	r.Attachments = map[string][]byte{}
	inputFiles := func(input *ChannelInput) error {
		if input == nil {
			return nil
		}
		for _, a := range input.Attachments {
			if a.Path == "" {
				continue
			}
			if _, ok := r.Attachments[a.Path]; ok {
				continue
			}
			info, e := os.Stat(a.Path)
			if e != nil {
				return fmt.Errorf("required input attachment %s: %w", a.Path, e)
			}
			if info.Size() > 64<<20 {
				return errors.New("required attachment exceeds 64 MiB backup limit")
			}
			b, e := readPrivateFile(a.Path)
			if e != nil {
				return e
			}
			if fileHash(b) != a.SHA256 || int64(len(b)) != a.Bytes {
				return fmt.Errorf("required attachment checksum mismatch: %s", a.Path)
			}
			r.Attachments[a.Path] = b
		}
		return nil
	}
	for _, run := range r.Runs {
		if run.Source != nil {
			if err = inputFiles(run.Source.Input); err != nil {
				return r, err
			}
		}
	}
	for _, message := range r.Messages {
		if err = inputFiles(message.Input); err != nil {
			return r, err
		}
	}
	for _, id := range recoveryKeys(sessions) {
		var s Session
		if err = read("sessions", id, &s); err != nil {
			return r, err
		}
		r.Sessions = append(r.Sessions, s)
	}
	for _, id := range recoveryKeys(receipts) {
		var receipt ExternalReceipt
		if err = read("external_memory_receipts", id, &receipt); err != nil {
			return r, err
		}
		r.Receipts = append(r.Receipts, receipt)
	}
	var externalTables bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='external_memory_connections')").Scan(&externalTables); err != nil {
		return r, err
	}
	query := "SELECT payload FROM external_memory_connections ORDER BY id"
	if !externalTables {
		query = "SELECT NULL WHERE 0"
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var b []byte
		var c ExternalConnection
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &c)
		}
		if err != nil {
			rows.Close()
			return r, err
		}
		r.Connections = append(r.Connections, c)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return r, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY id")
	if err != nil {
		return r, err
	}
	for rows.Next() {
		p, e := scanProject(rows)
		if e != nil {
			rows.Close()
			return r, e
		}
		r.Projects = append(r.Projects, p)
	}
	return r, errors.Join(rows.Err(), rows.Close())
}

func recoveryKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RestoreMemoryRecovery creates a fresh state home. Historical evidence is kept
// verbatim in recovery-original.json; operational copies cannot resume old work.
func RestoreMemoryRecovery(ctx context.Context, r MemoryRecovery, home, memoryRoot, cacheRoot string) error {
	if r.Version != 1 {
		return errors.New("unsupported memory recovery format")
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore state home must not exist")
	}
	if err := privateDirectories(home); err != nil {
		return err
	}
	home, err := physicalPath(home)
	if err != nil {
		return err
	}
	s, err := Open(home, false)
	if err != nil {
		return err
	}
	defer s.Close()
	if err = atomicJSON(filepath.Join(home, "recovery-original.json"), r, true); err != nil {
		return err
	}
	relocatedInputs := map[string]string{}
	for original, b := range r.Attachments {
		path := filepath.Join(home, "recovery-files", fileHash(b))
		if err = atomicFile(path, b, true); err != nil {
			return err
		}
		relocatedInputs[original] = path
	}
	remapInput := func(input *ChannelInput) error {
		if input == nil {
			return nil
		}
		for i, a := range input.Attachments {
			if a.Path == "" {
				continue
			}
			b, ok := r.Attachments[a.Path]
			if !ok || fileHash(b) != a.SHA256 || int64(len(b)) != a.Bytes {
				return errors.New("missing or corrupt recovery input attachment")
			}
			input.Attachments[i].Path = relocatedInputs[a.Path]
		}
		return nil
	}
	err = s.sqlTransaction(ctx, func(tx *sql.Tx) error {
		insert := func(table, id string, v any) error {
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO "+table+"(id,payload) VALUES(?,?)", id, b)
			return e
		}
		for _, p := range r.Projects {
			a, e := json.Marshal(p.Aliases)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO projects("+projectColumns+") VALUES(?,?,?,?,?,?,?,?)", p.ID, p.Name, p.Workspace, p.Scope, p.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), p.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), p.Description, a); e != nil {
				return e
			}
		}
		for _, v := range r.Sessions {
			v.NativeSessionID, v.RuntimeFingerprint, v.PreviousSessionID, v.RecoveryRunID = "", "", "", ""
			if e := insert("sessions", v.ID, v); e != nil {
				return e
			}
		}
		for _, v := range r.Runs {
			if v.Source != nil {
				if e := remapInput(v.Source.Input); e != nil {
					return e
				}
			}
			if v.ExecutionStatus == "running" {
				v.ExecutionStatus = "interrupted"
			}
			if v.MemoryStatus == "pending" || v.MemoryStatus == "failed" {
				v.MemoryStatus = "skipped"
			}
			if v.ExperienceStatus == "pending" || v.ExperienceStatus == "failed" {
				v.ExperienceStatus = "skipped"
			}
			v.NativeSessionID, v.RuntimeFingerprint = "", ""
			events := v.Events
			v.Events = nil
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO runs(id,session_id,scope,created_at,execution_status,memory_status,payload) VALUES(?,?,?,?,?,?,?)", v.ID, v.SessionID, v.Scope, timestamp(v.CreatedAt), v.ExecutionStatus, v.MemoryStatus, b); e != nil {
				return e
			}
			for _, ev := range events {
				b, e := json.Marshal(ev)
				if e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "INSERT INTO events(run_id,seq,payload) VALUES(?,?,?)", v.ID, ev.Seq, b); e != nil {
					return e
				}
			}
		}
		for _, v := range r.Candidates {
			if v.Status == "pending" {
				v.Status = "rejected"
			}
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO candidates(id,run_id,scope,status,payload) VALUES(?,?,?,?,?)", v.ID, v.RunID, v.Scope, v.Status, b); e != nil {
				return e
			}
		}
		for _, v := range r.Messages {
			if e := remapInput(v.Input); e != nil {
				return e
			}
			v.Status, v.ReplyStatus = "completed", "skipped"
			v.ReplyTarget, v.Reactions = nil, nil
			v.RuntimeFingerprint = ""
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO channel_messages(id,channel,account_id,message_id,profile,status,received_at,reply_url,payload) VALUES(?,?,?,?,?,?,?,?,?)", v.ID, v.Channel, v.AccountID, v.MessageID, v.Profile, v.Status, timestamp(v.ReceivedAt), "", b); e != nil {
				return e
			}
		}
		for _, v := range r.Connections {
			if e := insert("external_memory_connections", v.ID, v); e != nil {
				return e
			}
		}
		for _, v := range r.Receipts {
			if v.ProcessingStatus == "pending" || v.ProcessingStatus == "processing" {
				v.ProcessingStatus = "failed"
			}
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO external_memory_receipts(id,connection_id,submission_key,payload) VALUES(?,?,?,?)", v.ID, v.Connection.ID, v.Submission.Key, b); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c := FileMemoryConfig{Version: 1, MemoryRoot: memoryRoot, CacheRoot: cacheRoot, SourceID: r.Config.SourceID, StateHome: home}
	if err = validateFileConfig(c, home); err != nil {
		return err
	}
	f := &fileMemory{config: c, sourceDir: filepath.Join(memoryRoot, "sources", sourceKey(c.SourceID))}
	release, err := f.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err = os.Lstat(filepath.Join(f.sourceDir, "current.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore source already exists")
	}
	snap := fileSnapshot{current: r.Memories, revisions: r.Memories, evidence: r.Evidence, order: map[string]int64{}}
	seen := map[string]bool{}
	for i, m := range r.Memories {
		if seen[m.ID] {
			return errors.New("duplicate recovery memory")
		}
		seen[m.ID] = true
		snap.order[m.ID] = int64(i + 1)
	}
	if _, err = f.publish(snap, nil); err != nil {
		return err
	}
	if err = f.ack(f.manifest.Generation); err != nil {
		return err
	}
	if err = atomicJSON(FileMemoryMarker(home), c, true); err != nil {
		return err
	}
	_, err = f.readSnapshot()
	return err
}
