import Foundation
import ATCAPI

/// Persists each Workspace's last-selected session using the local
/// Connection UUID plus the server Workspace ID. Server IDs are only unique
/// within a Connection, so a bare Workspace ID is never a safe key.
struct WorkspaceSelectionMemory {
    private struct Record: Codable, Equatable {
        let connectionID: UUID
        let workspaceID: String
        var sessionID: String
    }

    private let defaults: UserDefaults
    private static let key = "workspaceSelections.v2"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func sessionID(for ref: WorkspaceRef) -> String? {
        stored().first {
            $0.connectionID == ref.connectionID && $0.workspaceID == ref.workspaceID
        }?.sessionID
    }

    func remember(sessionID: String, for ref: WorkspaceRef) {
        var records = stored()
        if let index = records.firstIndex(where: {
            $0.connectionID == ref.connectionID && $0.workspaceID == ref.workspaceID
        }) {
            guard records[index].sessionID != sessionID else { return }
            records[index].sessionID = sessionID
        } else {
            records.append(Record(
                connectionID: ref.connectionID,
                workspaceID: ref.workspaceID,
                sessionID: sessionID
            ))
        }
        save(records)
    }

    /// The selection to restore when opening a Workspace: the remembered
    /// session if it still exists in the store and still belongs to that
    /// Workspace (any lifecycle state); nil otherwise, which shows the
    /// Workspace empty state.
    func restoredSelection(for ref: WorkspaceRef, in sessions: [Session]) -> SessionRef? {
        guard let saved = sessionID(for: ref),
              let session = sessions.first(where: { $0.id == saved }),
              session.workspace?.id == ref.workspaceID
        else { return nil }
        return SessionRef(connectionID: ref.connectionID, sessionID: session.id)
    }

    func forget(_ ref: WorkspaceRef) {
        var records = stored()
        let previousCount = records.count
        records.removeAll {
            $0.connectionID == ref.connectionID && $0.workspaceID == ref.workspaceID
        }
        guard records.count != previousCount else { return }
        save(records)
    }

    func forget(connectionID: UUID) {
        var records = stored()
        let previousCount = records.count
        records.removeAll { $0.connectionID == connectionID }
        guard records.count != previousCount else { return }
        save(records)
    }

    private func stored() -> [Record] {
        guard let data = defaults.data(forKey: Self.key),
              let records = try? JSONDecoder().decode([Record].self, from: data)
        else { return [] }
        return records
    }

    private func save(_ records: [Record]) {
        guard let data = try? JSONEncoder().encode(records) else { return }
        defaults.set(data, forKey: Self.key)
    }
}
