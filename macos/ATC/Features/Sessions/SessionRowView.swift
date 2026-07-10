import SwiftUI
import ATCAPI

struct SessionRowView: View {
    let session: Session
    /// Whether the app currently holds a live attach for this session.
    var isConnected = false
    /// Rows nested under a project inherit its directory, so they show
    /// recency instead of repeating the path.
    var showsWorkingDir = true

    var body: some View {
        HStack {
            StatusBadge(session: session)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.displayName)
                    .lineLimit(1)
                Text(caption)
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

    private var caption: String {
        if showsWorkingDir { return session.workingDir }
        return "\(session.action) · \(session.updatedAt.formatted(.relative(presentation: .named)))"
    }
}
