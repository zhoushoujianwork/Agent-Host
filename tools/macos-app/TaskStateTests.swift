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
        print("TaskStateTests passed: mapping, identity, outcomes, control, persistence, deduplication")
    }
}
