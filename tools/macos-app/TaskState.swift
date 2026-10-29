import Foundation

// Presentation projections only. Host records remain the authority for all outcomes.
struct TaskRecord {
    let key: String
    let run: [String: Any]
    let events: [[String: Any]]
    let control: [String: Any]
    var status: String { run["execution_status"] as? String ?? "unknown" }
    var title: String { String((run["input"] as? String ?? key).prefix(120)) }
    var agent: String { run["agent_id"] as? String ?? "unknown" }
    var active: Bool { control["active"] as? Bool ?? false }
    var canStop: Bool { active && control["can_stop"] as? Bool == true && !["stopping", "completed", "failed", "canceled"].contains(status) }
    var destination: String { Self.destination(for: key) }
    static func destination(for key: String) -> String {
        var query = URLComponents()
        query.queryItems = [URLQueryItem(name: "key", value: key)]
        return "#executions?" + (query.percentEncodedQuery ?? "")
    }
    var progress: String {
        events.reversed().compactMap { $0["message"] as? String }.first(where: { !$0.isEmpty }) ?? status
    }
    var outcomes: String {
        "Execution: \(status) · Acceptance: \(run["acceptance_status"] as? String ?? "unknown") · Memory: \(run["memory_status"] as? String ?? "unknown")"
    }
    var inbox: [InboxEvent] {
        events.compactMap { event in
            guard let seq = event["seq"] as? Int, let type = event["type"] as? String else { return nil }
            let kind: String
            switch type {
            case "run.started": kind = "started"
            case "execution.ready", "run.finished":
                let data = event["data"] as? [String: Any] ?? [:]
                let final = data["execution_status"] as? String ?? status
                if type == "run.finished" && events.contains(where: { $0["type"] as? String == "execution.ready" }) { return nil }
                kind = ["completed", "failed", "canceled"].contains(final) ? (final == "canceled" ? "stopped" : final) : "unknown"
            // ACP records blocked permission requests; this is a conversation destination,
            // never an approval button or an assertion that the engine is still waiting.
            case "native.evidence":
                guard let data = event["data"] as? [String: Any],
                      data["native_type"] as? String == "acp_update",
                      let raw = data["raw"] as? [String: Any],
                      raw["sessionUpdate"] as? String == "permission_request",
                      let outcome = raw["outcome"] as? [String: Any],
                      outcome["outcome"] as? String == "cancelled" else { return nil }
                kind = "input-needed"
            default: return nil
            }
            return InboxEvent(id: "\(key):\(seq)", key: key, kind: kind, title: title,
                              at: event["at"] as? String ?? "")
        }
    }
}

struct InboxEvent: Codable, Equatable {
    let id: String
    let key: String
    let kind: String
    let title: String
    let at: String
    var requestsAttention: Bool { ["failed", "input-needed", "unknown"].contains(kind) }
    func shouldNotify(completions: Bool) -> Bool { kind != "completed" || completions }
}

struct InboxJournal: Codable {
    var entries: [InboxEvent] = []
    var seen: Set<String> = []
    mutating func ingest(_ events: [InboxEvent]) -> [InboxEvent] {
        let fresh = events.filter { seen.insert($0.id).inserted }
        entries = Array((entries + fresh).sorted { $0.at > $1.at }.prefix(200))
        return fresh
    }
}
