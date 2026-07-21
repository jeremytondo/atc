import SwiftUI

struct CommandFeedbackOverlay: View {
    @Environment(WindowKeyboardRouter.self) private var router
    @Environment(ConfigurationStore.self) private var configStore

    var body: some View {
        ZStack {
            if let notice = configStore.notice {
                VStack {
                    ConfigNoticeView(notice: notice) {
                        configStore.dismissNotice()
                    }
                    Spacer()
                }
                .padding(.top, 8)
            }

            Group {
                if let flash = router.flash {
                    RouterFlashView(flash: flash)
                } else if router.pendingNode != nil {
                    CommandSequenceHintView()
                }
            }
            .allowsHitTesting(false)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomTrailing)
            .padding([.bottom, .trailing], 24)
        }
    }
}

struct CommandSequenceHintView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(ConfigurationStore.self) private var configStore
    @Environment(WindowKeyboardRouter.self) private var router

    private var continuations: [(KeyStroke, CommandID)] {
        guard let node = router.pendingNode else { return [] }
        return node.compactMap { stroke, node in
            guard case .command(let command) = node else { return nil }
            return (stroke, command)
        }.sorted { $0.0.description < $1.0.description }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(continuations, id: \.0) { stroke, command in
                continuationRow(stroke: stroke, command: command)
            }
        }
        .frame(minWidth: 280, alignment: .leading)
        .padding(.horizontal, 18)
        .padding(.vertical, 16)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 14))
        .shadow(color: .black.opacity(0.2), radius: 12, y: 4)
    }

    @ViewBuilder
    private func continuationRow(stroke: KeyStroke, command: CommandID) -> some View {
        let context = CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
        let descriptor = CommandRegistry.descriptor(for: command)
        let availability = descriptor.availability(context)
        HStack(spacing: 11) {
            Text(stroke.displayDescription)
                .font(.callout.monospaced().weight(.semibold))
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .background(.quaternary, in: RoundedRectangle(cornerRadius: 6))
            Text(descriptor.title)
                .font(.callout)
            if case .unavailable(let reason) = availability {
                Text(reason)
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        }
        .foregroundStyle(availability.isAvailable ? .primary : .secondary)
        .opacity(availability.isAvailable ? 1 : 0.6)
    }
}

struct RouterFlashView: View {
    let flash: RouterFlash

    var body: some View {
        Text(flash.message)
            .font(.callout.weight(.medium))
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
            .background(.thinMaterial, in: Capsule())
            .shadow(color: .black.opacity(0.2), radius: 12, y: 4)
    }
}

struct ConfigNoticeView: View {
    let notice: ConfigNotice
    let dismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
            Text(notice.message)
                .font(.callout)
                // Parser messages can run long; keep the banner bounded.
                .lineLimit(3)
                .frame(maxWidth: 560, alignment: .leading)
            Button("Dismiss", action: dismiss)
                .buttonStyle(.borderless)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
        .shadow(color: .black.opacity(0.2), radius: 10, y: 3)
        .padding(.horizontal, 16)
    }
}
