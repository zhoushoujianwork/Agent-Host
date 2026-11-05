import Foundation

@main struct TaskStateTests {
    static func main() throws {
        func record(_ status: String = "running", active: Bool = true, stop: Bool = true, events: [[String: Any]] = []) -> TaskRecord {
            TaskRecord(key: "feishu:db:a & b", run: ["execution_status": status, "input": "Fixture", "agent_id": "agent", "acceptance_status": "unverified", "memory_status": "pending"], events: events, control: ["active": active, "can_stop": stop])
        }
        let events: [[String: Any]] = [
            ["seq": 1, "type": "run.started", "at": "1"],
            ["seq": 2, "type": "engine.progress", "message": "waiting for input"],
            ["seq": 3, "type": "native.evidence", "at": "3", "data": ["native_type": "acp_update", "raw": ["sessionUpdate": "permission_request", "outcome": ["outcome": "cancelled"]]]],
            ["seq": 4, "type": "run.finished", "at": "4", "data": ["execution_status": "canceled"]]]
        let task = record("canceled", active: false, events: events)
        precondition(task.inbox.map(\.kind) == ["started", "input-needed", "stopped"])
        precondition(task.inbox.allSatisfy { $0.key == task.key })
        precondition(task.inbox[1].requestsAttention)
        let query = URLComponents(string: "http://localhost/" + task.destination)!.percentEncodedFragment!.split(separator: "?", maxSplits: 1)[1]
        precondition(URLComponents(string: "?" + query)!.queryItems!.first!.value == task.key)
        precondition(!task.canStop && record().canStop && !record(stop: false).canStop)
        precondition(!record("stopping").canStop && !record("completed", active: false).canStop)
        precondition(!record("completed").canStop && !record("canceled").canStop)
        precondition(task.outcomes.contains("unverified") && task.outcomes.contains("pending"))
        precondition(record("failed", events: [["seq": 1, "type": "run.finished"]]).inbox[0].kind == "failed")
        precondition(record("unknown", events: [["seq": 1, "type": "run.finished"]]).inbox[0].kind == "unknown")
        let completion = record("completed", events: [["seq": 1, "type": "run.finished"]]).inbox[0]
        precondition(!completion.shouldNotify(completions: false) && completion.shouldNotify(completions: true))
        func event(_ id: String, _ kind: String, key: String = "k", at: String = "2026-01-01T00:00:00Z") -> InboxEvent {
            InboxEvent(id: id, key: key, kind: kind, title: "T \(key)", at: at)
        }
        let closeout = record("completed", events: [["seq": 1, "type": "execution.ready"], ["seq": 2, "type": "run.finished"]])
        precondition(closeout.inbox.count == 1 && closeout.inbox[0].kind == "completed")
        let bypassed = record(events: [["seq": 5, "type": "native.evidence", "data": ["native_type": "acp_update", "raw": ["sessionUpdate": "permission_request", "outcome": ["outcome": "selected"]]]]])
        precondition(bypassed.inbox.isEmpty)
        var journal = InboxJournal()
        precondition(journal.ingest(task.inbox).count == 3)
        precondition(journal.ingest(task.inbox).isEmpty)
        journal = try JSONDecoder().decode(InboxJournal.self, from: JSONEncoder().encode(journal))
        precondition(journal.ingest(task.inbox).isEmpty)
        let other = TaskRecord(key: "cli:db:a & b", run: task.run, events: events, control: [:])
        precondition(journal.ingest(other.inbox).count == 3 && !other.canStop)

        // Notification policy: failed/input-needed by default, unknown in-app, completion opt-in, never started/stopped.
        for (kind, plain, opted) in [("failed", true, true), ("input-needed", true, true), ("unknown", false, false),
                                     ("completed", false, true), ("started", false, false), ("stopped", false, false)] {
            precondition(event("x", kind).shouldNotify(completions: false) == plain && event("x", kind).shouldNotify(completions: true) == opted)
        }

        // Freshness uses parsed instants, so offsets and fractions compare correctly.
        let launch = InboxEvent.parse("2026-01-01T10:00:00Z")!
        precondition(InboxEvent.parse("2026-01-01T17:59:59+08:00")! < launch)
        precondition(InboxEvent.parse("2026-01-01T04:00:01-06:00")! == launch + 1)
        precondition(InboxEvent.parse("2026-01-01T10:00:00.123456789Z")! > launch && InboxEvent.parse("not a date") == nil)
        precondition(InboxEvent.parse("2026-01-01T10:00:00.Z") == nil)
        precondition(!event("a", "failed", at: "2026-01-01T17:59:59+08:00").shouldDeliver(completions: false, since: launch, viewing: nil))
        precondition(event("a", "failed", at: "2026-01-01T04:00:01-06:00").shouldDeliver(completions: false, since: launch, viewing: nil))
        precondition(!event("a", "failed", at: "garbage").shouldDeliver(completions: false, since: launch, viewing: nil))
        precondition(!event("a", "failed", at: "2026-01-01T10:00:01Z").shouldDeliver(completions: false, since: launch, viewing: "k"))
        precondition(event("a", "failed", at: "2026-01-01T10:00:01Z").shouldDeliver(completions: false, since: launch, viewing: "other"))

        // Legacy journals (no read/ack fields) load unread with nothing acknowledged and keep dedupe.
        let legacy = #"{"entries":[{"id":"k:1","key":"k","kind":"failed","title":"T","at":"2026-01-01T00:00:00Z"}],"seen":["k:1","gone:9"]}"#
        var old = try JSONDecoder().decode(InboxJournal.self, from: Data(legacy.utf8))
        precondition(old.readIDs.isEmpty && old.acknowledgedIDs.isEmpty && old.unreadCount == 1 && old.attentionCount == 1)
        precondition(old.ingest([event("gone:9", "failed", key: "gone")]).isEmpty)

        // Grouping, read, acknowledge, reingest and new events on the same task.
        var inbox = InboxJournal()
        _ = inbox.ingest([event("k:1", "started", at: "2026-01-01T00:00:01Z"), event("k:2", "failed", at: "2026-01-01T00:00:02Z"),
                          event("m:1", "completed", key: "m", at: "2026-01-01T00:00:03Z"), event("n:1", "unknown", key: "n", at: "2026-01-01T00:00:04Z")])
        precondition(inbox.tasks().map(\.key) == ["n", "m", "k"] && inbox.tasks()[2].latest.id == "k:2" && inbox.tasks()[2].eventCount == 2)
        precondition(inbox.attentionCount == 2 && inbox.unreadCount == 3)
        precondition(inbox.tasks(attentionOnly: true).map(\.key) == ["n", "k"])
        precondition(inbox.tasks(query: "COMPLETED").map(\.key) == ["m"] && inbox.tasks(attentionOnly: true, query: "m").isEmpty)
        precondition(inbox.markRead("k") && !inbox.markRead("k"))
        precondition(inbox.unreadCount == 2 && inbox.attentionCount == 2)
        precondition(inbox.acknowledge("k") && !inbox.acknowledge("k") && !inbox.acknowledge("m"))
        precondition(inbox.attentionCount == 1 && inbox.tasks().first { $0.key == "k" }!.unread == false)
        precondition(inbox.ingest(task.inbox.isEmpty ? [] : [event("k:2", "failed", at: "2026-01-01T00:00:02Z")]).isEmpty)
        precondition(inbox.attentionCount == 1)
        precondition(inbox.ingest([event("k:3", "failed", at: "2026-01-01T00:00:09Z")]).count == 1)
        precondition(inbox.attentionCount == 2 && inbox.tasks()[0].key == "k" && inbox.tasks()[0].unread)
        let restored = try JSONDecoder().decode(InboxJournal.self, from: JSONEncoder().encode(inbox))
        precondition(restored.readIDs == inbox.readIDs && restored.acknowledgedIDs == inbox.acknowledgedIDs && restored.attentionCount == 2)
        // Ordering follows instants, not strings, and pruning keeps state only for retained entries.
        var ordered = InboxJournal()
        _ = ordered.ingest([event("a", "failed", key: "a", at: "2026-01-01T09:00:00+08:00"), event("b", "failed", key: "b", at: "2026-01-01T02:00:00Z")])
        precondition(ordered.tasks().map(\.key) == ["b", "a"])
        ordered.markRead("a"); ordered.acknowledge("a")
        _ = ordered.ingest((0..<InboxJournal.retained).map { event("p:\($0)", "started", key: "p", at: "2026-01-02T00:00:00Z") })
        precondition(ordered.entries.count == InboxJournal.retained && ordered.readIDs.isEmpty && ordered.acknowledgedIDs.isEmpty)
        precondition(ordered.ingest([event("a", "failed", key: "a")]).isEmpty)
        print("TaskStateTests passed: mapping, identity, outcomes, control, persistence, deduplication, read/ack, notification policy")
    }
}
