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
    var label: String {
        switch kind {
        case "started": return "Started"
        case "completed": return "Completed"
        case "failed": return "Failed"
        case "stopped": return "Stopped"
        case "input-needed": return "Needs review"
        default: return "Outcome unknown"
        }
    }
    var requestsAttention: Bool { ["failed", "input-needed", "unknown"].contains(kind) }
    var date: Date? { Self.parse(at) }
    /// Failed and input-needed events alert by default; unknown outcomes stay in the inbox.
    /// Started and stopped events never alert; completions are opt-in.
    func shouldNotify(completions: Bool) -> Bool {
        ["failed", "input-needed"].contains(kind) || (kind == "completed" && completions)
    }
    /// Delivery needs a parsed time at or after launch and a task the user is not already viewing.
    func shouldDeliver(completions: Bool, since launch: Date, viewing viewedKey: String?) -> Bool {
        guard shouldNotify(completions: completions), let date, date >= launch else { return false }
        return viewedKey != key
    }
    /// Parses RFC 3339 with any offset. Fractions beyond milliseconds are truncated.
    static func parse(_ value: String) -> Date? {
        var text = value
        var fractional = false
        if let dot = text.firstIndex(of: ".") {
            let after = text[text.index(after: dot)...]
            let digits = after.prefix { $0.isASCII && $0.isNumber }
            guard !digits.isEmpty else { return nil }
            let millis = String(digits.prefix(3)).padding(toLength: 3, withPad: "0", startingAt: 0)
            text = String(text[..<dot]) + "." + millis + String(after.dropFirst(digits.count))
            fractional = true
        }
        return (fractional ? fractionalParser : plainParser).date(from: text)
    }
    private static let plainParser: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter(); formatter.formatOptions = [.withInternetDateTime]; return formatter
    }()
    private static let fractionalParser: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter(); formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]; return formatter
    }()
}

/// One inbox row: every retained event of a stable task key.
struct InboxTask: Equatable {
    let key: String
    let latest: InboxEvent
    let eventCount: Int
    let unread: Bool
    let needsAttention: Bool
}

struct InboxJournal: Codable {
    static let retained = 200
    var entries: [InboxEvent] = []
    var seen: Set<String> = []
    /// Per-event state. Journals written before these fields existed load as all unread, none acknowledged.
    var readIDs: Set<String> = []
    var acknowledgedIDs: Set<String> = []

    init() {}
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        entries = try values.decode([InboxEvent].self, forKey: .entries)
        seen = try values.decode(Set<String>.self, forKey: .seen)
        readIDs = try values.decodeIfPresent(Set<String>.self, forKey: .readIDs) ?? []
        acknowledgedIDs = try values.decodeIfPresent(Set<String>.self, forKey: .acknowledgedIDs) ?? []
    }

    mutating func ingest(_ events: [InboxEvent]) -> [InboxEvent] {
        let fresh = events.filter { seen.insert($0.id).inserted }
        if fresh.isEmpty { return fresh }
        let dated = (entries + fresh).map { ($0.date ?? .distantPast, $0.at, $0.id, $0) }
        entries = dated.sorted { ($0.0, $0.1, $0.2) > ($1.0, $1.1, $1.2) }.prefix(Self.retained).map { $0.3 }
        // Dropped events lose read/ack state; `seen` keeps them from being re-ingested.
        let ids = Set(entries.map { $0.id })
        readIDs.formIntersection(ids); acknowledgedIDs.formIntersection(ids)
        return fresh
    }

    func tasks(attentionOnly: Bool = false, query: String = "") -> [InboxTask] {
        var order: [String] = []
        var groups: [String: [InboxEvent]] = [:]
        for event in entries {
            if groups[event.key] == nil { order.append(event.key) }
            groups[event.key, default: []].append(event)
        }
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return order.compactMap { key in
            let events = groups[key]!
            let task = InboxTask(key: key, latest: events[0], eventCount: events.count,
                                 unread: events.contains { !readIDs.contains($0.id) },
                                 needsAttention: events.contains { $0.requestsAttention && !acknowledgedIDs.contains($0.id) })
            if attentionOnly && !task.needsAttention { return nil }
            if !needle.isEmpty && ![key, task.latest.title, task.latest.kind].contains(where: { $0.lowercased().contains(needle) }) { return nil }
            return task
        }
    }
    /// Tasks with an unacknowledged attention event, independent of read state.
    var attentionCount: Int { tasks(attentionOnly: true).count }
    var unreadCount: Int { tasks().filter { $0.unread }.count }

    /// Marks every current event of the task read. Later events remain unread.
    @discardableResult mutating func markRead(_ key: String) -> Bool {
        let before = readIDs.count
        readIDs.formUnion(entries.filter { $0.key == key }.map { $0.id })
        return readIDs.count != before
    }
    /// Acknowledges every current attention event of the task. Later attention events stay open.
    @discardableResult mutating func acknowledge(_ key: String) -> Bool {
        let before = acknowledgedIDs.count
        acknowledgedIDs.formUnion(entries.filter { $0.key == key && $0.requestsAttention }.map { $0.id })
        return acknowledgedIDs.count != before
    }
}
