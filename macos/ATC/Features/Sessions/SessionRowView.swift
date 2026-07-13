import SwiftUI
import ATCAPI

struct SessionRowView: View {
    let session: Session
    /// Whether the app currently holds a live attach for this session.
    var isConnected = false
    /// Rows nested under a project inherit its directory, so they show
    /// recency instead of repeating the path.
    var showsWorkingDir = true
    /// Overrides for callers that resolve names against the action
    /// registry (the Workspace Navigator); nil falls back to the session's own
    /// labels.
    var title: String?
    var caption: String?

    var body: some View {
        HStack {
            StatusBadge(session: session)
            VStack(alignment: .leading, spacing: 2) {
                Text(title ?? session.displayName)
                    .lineLimit(1)
                Text(caption ?? defaultCaption)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(showsWorkingDir ? .head : .tail)
            }
            Spacer()
            if isConnected {
                Image(systemName: "terminal")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help("Connected")
            }
        }
        .padding(.vertical, 2)
    }

    private var defaultCaption: String {
        if showsWorkingDir { return session.workingDir }
        return "\(session.actionLabel) · \(session.updatedAt.formatted(.relative(presentation: .named)))"
    }
}
