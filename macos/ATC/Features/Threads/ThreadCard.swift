import ATCAppServerAPI
import SwiftUI

/// One thread in the sidebar. The whole card opens the thread; Pin and
/// Archive stay out of sight until hover or keyboard focus, and remain
/// independently clickable and tabbable when shown.
struct ThreadCard: View {
    let item: ThreadListItem
    let reachability: Reachability
    let isSelected: Bool
    let isBusy: Bool
    let canMutate: Bool
    /// Non-nil while exact ⌘ is held and this card holds a numbered slot.
    let shortcutLabel: String?
    @FocusState.Binding var focusedThread: ThreadRef?
    let onOpen: () -> Void
    let onRename: () -> Void
    let onTogglePin: () -> Void
    let onArchive: () -> Void

    @State private var isHovering = false

    private var isFocused: Bool { focusedThread == item.ref }

    var body: some View {
        let thread = item.thread
        // One Button for the whole card: it carries the click and the
        // accessibility action. Return is handled on the composite below — a
        // nested .plain Button is not activated by the focused container.
        // The hover chips are Buttons themselves, so they sit in an overlay
        // rather than nested in this label.
        Button {
            focusedThread = item.ref
            onOpen()
        } label: {
            VStack(alignment: .leading, spacing: Spacing.xs) {
                // Top row: Project name as compact context; the trailing area
                // shows the Connection at rest and the actions on hover/focus.
                HStack(alignment: .center, spacing: Spacing.sm) {
                    Text(item.projectLabel)
                        .font(.caption.weight(.semibold))
                        .lineLimit(1)
                    Spacer(minLength: Spacing.sm)
                    trailingSlot
                }

                // The dominant element.
                Text(thread.displayName)
                    .font(.body.weight(.medium))
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)

                // Bottom metadata row: working directory when it differs from
                // the Project default; activity state and Agent trailing.
                HStack(alignment: .center, spacing: Spacing.sm) {
                    if let directory = item.distinctWorkingDirectory {
                        Text(directory)
                            .font(.caption.monospaced())
                            .foregroundStyle(.tertiary)
                            .lineLimit(1)
                            .truncationMode(.head)
                    }
                    Spacer(minLength: Spacing.sm)
                    if let status = thread.activityState.statusLabel {
                        Text(status)
                            .font(.caption.weight(.medium))
                            .foregroundStyle(thread.activityState.statusColor)
                    }
                    agentBadge
                }
            }
            .padding(Spacing.md)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        .background {
            RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
                .fill(backgroundStyle)
        }
        .overlay {
            RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
                .strokeBorder(borderColor, lineWidth: 1)
        }
        .overlay(alignment: .topTrailing) { hoverActions }
        .opacity(isBusy ? Dimming.unavailable : 1)
        .onHover { isHovering = $0 }
        .focusable()
        .focusEffectDisabled()
        .focused($focusedThread, equals: item.ref)
        .onKeyPress(.return) {
            onOpen()
            return .handled
        }
        .contextMenu {
            Button("Rename…", systemImage: "pencil", action: onRename)
                .disabled(!canMutate || isBusy)
            Button(
                thread.isPinned ? "Unpin" : "Pin",
                systemImage: thread.isPinned ? "pin.slash" : "pin",
                action: onTogglePin
            )
            .disabled(!canMutate || isBusy)
            Button("Archive", systemImage: "archivebox", action: onArchive)
                .disabled(!canMutate || isBusy || thread.activityState == .working)
        }
        .navigatorListRow(top: Spacing.xs, bottom: Spacing.xs)
    }

    /// The card's trailing slot: the held-modifier badge, the Connection at
    /// rest, or the space the hover actions take over. Everything here is the
    /// same height, so the card never resizes under the pointer.
    @ViewBuilder
    private var trailingSlot: some View {
        if let shortcutLabel {
            ShortcutBadge(label: shortcutLabel)
        } else if isHovering || isFocused {
            Color.clear
                .frame(
                    width: NavigatorMetrics.actionSize * 2 + Spacing.xs,
                    height: NavigatorMetrics.actionSize
                )
        } else {
            HStack(spacing: Spacing.xs) {
                Text(item.connectionName)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                StatusDot(reachability: reachability, size: .inline)
            }
            .frame(height: NavigatorMetrics.actionSize)
        }
    }

    /// Pin and Archive, over the slot `trailingSlot` reserves for them. The
    /// held-modifier badge wins that slot.
    @ViewBuilder
    private var hoverActions: some View {
        if shortcutLabel == nil, isHovering || isFocused {
            HStack(spacing: Spacing.xs) {
                cardAction(
                    systemImage: item.thread.isPinned ? "pin.slash" : "pin",
                    help: item.thread.isPinned ? "Unpin" : "Pin",
                    isEnabled: canMutate && !isBusy,
                    action: onTogglePin
                )
                cardAction(
                    systemImage: "archivebox",
                    help: "Archive",
                    // The server refuses archiving a working thread; the
                    // control is disabled rather than surfacing that as an
                    // error the user did not ask for.
                    isEnabled: canMutate && !isBusy
                        && item.thread.activityState != .working,
                    action: onArchive
                )
            }
            .padding(Spacing.md)
        }
    }

    /// The Agent icon in a small rounded-square badge, lower-trailing per
    /// the design.
    private var agentBadge: some View {
        Image(systemName: item.thread.agentId.systemImage)
            .font(.caption2)
            .foregroundStyle(.secondary)
            .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
            .background(
                Surface.raised,
                in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
            )
            .accessibilityLabel(item.thread.agentId.displayName)
    }

    private func cardAction(
        systemImage: String,
        help: String,
        isEnabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.caption)
                .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
                .background(
                    Surface.raised,
                    in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
                )
                .contentShape(RoundedRectangle(cornerRadius: Radius.control, style: .continuous))
        }
        .buttonStyle(.plain)
        .foregroundStyle(.secondary)
        .disabled(!isEnabled)
        .help(help)
        .accessibilityLabel(help)
    }

    /// Cards read as bordered containers at rest; selection is the accent
    /// border plus tint, hover a brighter neutral fill.
    private var backgroundStyle: AnyShapeStyle {
        if isSelected { return AnyShapeStyle(Color.accentColor.opacity(0.13)) }
        if isHovering { return AnyShapeStyle(Surface.raised) }
        return AnyShapeStyle(Surface.card)
    }

    private var borderColor: Color {
        if isSelected || isFocused { return Color.accentColor }
        return Surface.cardBorder
    }
}
