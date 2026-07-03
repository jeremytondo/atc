import SwiftUI
import CockpitAPI

struct SessionRowView: View {
    let session: Session
    /// Whether the app currently holds a live attach for this session.
    var isConnected = false

    var body: some View {
        HStack {
            StatusBadge(session: session)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.displayName)
                    .lineLimit(1)
                Text(session.workingDir)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.head)
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
}
