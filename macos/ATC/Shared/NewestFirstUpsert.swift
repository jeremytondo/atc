// The insertion rule the newest-first stores share: a mutation's response is
// merged into the cached list without waiting for a refresh, and a record the
// list has never seen takes the position its creation time earns.

import ATCAppServerAPI
import Foundation

/// A server record a store keeps ordered newest-created-first.
protocol NewestFirstRecord {
    var id: String { get }
    var createdAt: Date { get }
}

extension Project: NewestFirstRecord {}
extension Terminal: NewestFirstRecord {}

extension Array where Element: NewestFirstRecord {
    /// Replaces the record with the same id in place, or inserts ahead of the
    /// first older record (appending when it is the oldest of all).
    mutating func upsertNewestFirst(_ record: Element) {
        if let index = firstIndex(where: { $0.id == record.id }) {
            self[index] = record
            return
        }
        let index = firstIndex { $0.createdAt < record.createdAt }
        insert(record, at: index ?? endIndex)
    }
}
