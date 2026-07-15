import SwiftUI

struct CommandFeedbackOverlay: View {
    @Environment(WindowKeyboardRouter.self) private var router
    @Environment(KeyboardConfigStore.self) private var configStore

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
    @Environment(KeyboardConfigStore.self) private var configStore
    @Environment(WindowKeyboardRouter.self) private var router

    private var continuations: [(KeyStroke, CommandID)] {
        guard let node = router.pendingNode else { return [] }
        return node.compactMap { stroke, node in
            guard case .command(let command) = node else { return nil }
            return (stroke, command)
        }.sorted { $0.0.description < $1.0.description }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("Command Sequence")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            ForEach(continuations, id: \.0) { stroke, command in
                continuationRow(stroke: stroke, command: command)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
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
        HStack(spacing: 9) {
            Text(stroke.displayDescription)
                .font(.caption.monospaced().weight(.semibold))
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(.quaternary, in: RoundedRectangle(cornerRadius: 5))
            Text(descriptor.title)
                .font(.caption)
            if case .unavailable(let reason) = availability {
                Text(reason)
                    .font(.caption2)
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
            Image(systemName: "keyboard.badge.exclamationmark")
            Text(notice.message)
                .font(.callout)
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
