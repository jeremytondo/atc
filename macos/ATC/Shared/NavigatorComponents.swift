import SwiftUI

/// Shared geometry for every Navigator. Sidebar-specific values live here so
/// individual views only compose rows and never invent their own insets.
enum NavigatorMetrics {
    static let rowHeight: CGFloat = 28
    static let iconWidth: CGFloat = 18
    static let actionSize: CGFloat = 22
    static let nestedIndent: CGFloat = iconWidth + Spacing.sm
}

/// The standard interactive row used by all Navigators. Actions stay out of
/// sight until hover while the primary hit target remains full width.
struct NavigatorRow<Content: View, Actions: View>: View {
    let isSelected: Bool
    let isEnabled: Bool
    let leadingIndent: CGFloat
    let action: () -> Void
    let content: Content
    let actions: Actions

    @State private var isHovering = false

    init(
        isSelected: Bool = false,
        isEnabled: Bool = true,
        leadingIndent: CGFloat = 0,
        action: @escaping () -> Void,
        @ViewBuilder content: () -> Content,
        @ViewBuilder actions: () -> Actions
    ) {
        self.isSelected = isSelected
        self.isEnabled = isEnabled
        self.leadingIndent = leadingIndent
        self.action = action
        self.content = content()
        self.actions = actions()
    }

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Button(action: action) {
                content
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!isEnabled)

            if isHovering && isEnabled {
                HStack(spacing: Spacing.xs) {
                    actions
                }
                .transition(.opacity)
            }
        }
        .padding(.leading, leadingIndent)
        .frame(minHeight: NavigatorMetrics.rowHeight)
        .foregroundStyle(isEnabled ? AnyShapeStyle(.primary) : AnyShapeStyle(.tertiary))
        .background {
            if isHovering && !isSelected && isEnabled {
                RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
                    .fill(.quaternary)
            }
        }
        .contentShape(Rectangle())
        .onHover { isHovering = $0 }
        .navigatorListRow()
    }
}

struct NavigatorIconLabel: View {
    let title: String
    let systemImage: String

    var body: some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: systemImage)
                .frame(width: NavigatorMetrics.iconWidth)
                .foregroundStyle(.secondary)
            Text(title)
                .lineLimit(1)
        }
    }
}

struct NavigatorActionButton: View {
    let systemImage: String
    let help: String
    var isEnabled = true
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundStyle(.secondary)
        .disabled(!isEnabled)
        .help(help)
    }
}

struct NavigatorActionMenu<Content: View>: View {
    let systemImage: String
    let help: String
    let content: Content

    init(
        systemImage: String,
        help: String,
        @ViewBuilder content: () -> Content
    ) {
        self.systemImage = systemImage
        self.help = help
        self.content = content()
    }

    var body: some View {
        Menu {
            content
        } label: {
            Image(systemName: systemImage)
                .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
                .contentShape(Rectangle())
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .foregroundStyle(.secondary)
        .help(help)
    }
}

struct NavigatorSectionHeader: View {
    let title: String

    var body: some View {
        Text(title)
            .font(.headline)
            .foregroundStyle(.secondary)
            .textCase(nil)
            .padding(.top, Spacing.sm)
    }
}

extension View {
    func navigatorList() -> some View {
        listStyle(.sidebar)
            .environment(\.defaultMinListRowHeight, NavigatorMetrics.rowHeight)
    }

    func navigatorListRow() -> some View {
        listRowInsets(EdgeInsets(
            top: 1,
            leading: Spacing.sm,
            bottom: 1,
            trailing: Spacing.sm
        ))
    }
}
