import ATCDesign
import SwiftUI

/// Shared geometry for every Navigator. Sidebar-specific values live here so
/// individual views only compose rows and never invent their own insets.
enum NavigatorMetrics {
    static let rowHeight: CGFloat = 28
    static let iconWidth: CGFloat = 18
    static let actionSize: CGFloat = 22
    /// Bordered chip controls: the New Thread compose button, the thread
    /// filter, and New Project.
    static let chipSize: CGFloat = 32
    static let horizontalPadding = Spacing.md
    /// Keeps plain-row text aligned with card interiors: outer padding plus
    /// this equals the outer padding plus a card's internal padding.
    static let contentHorizontalPadding = Spacing.md
    static let rowVerticalInset: CGFloat = 1
}

/// A Navigator-owned scrolling container. A native sidebar `List` applies
/// outline-row margins outside our view hierarchy, which prevents custom row
/// surfaces and hit targets from using the full available width.
struct NavigatorList<Content: View>: View {
    let content: Content

    init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                content
            }
            // Lets a caller's `.scrollPosition(id:)` address the rows, which
            // is what replaces `List`'s scroll-to-selection here.
            .scrollTargetLayout()
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, NavigatorMetrics.horizontalPadding)
        }
    }
}

/// The standard interactive row used by all Navigators. Actions stay out of
/// sight until hover while the primary hit target remains full width.
struct NavigatorRow<Content: View, Actions: View>: View {
    let isSelected: Bool
    let isEnabled: Bool
    let leadingIndent: CGFloat
    let action: () -> Void
    let content: (Bool) -> Content
    let actions: Actions

    @State private var isHovering = false

    init(
        isSelected: Bool = false,
        isEnabled: Bool = true,
        leadingIndent: CGFloat = 0,
        action: @escaping () -> Void,
        @ViewBuilder content: @escaping (Bool) -> Content,
        @ViewBuilder actions: () -> Actions
    ) {
        self.isSelected = isSelected
        self.isEnabled = isEnabled
        self.leadingIndent = leadingIndent
        self.action = action
        self.content = content
        self.actions = actions()
    }

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Button(action: action) {
                content(isHovering)
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
        .navigatorSurface(isActive: isSelected || (isHovering && isEnabled))
        .contentShape(Rectangle())
        .onHover { isHovering = $0 }
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        .navigatorListRow()
    }
}

struct NavigatorIconLabel: View {
    let title: String
    let systemImage: String

    var body: some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: systemImage)
                .frame(width: NavigatorMetrics.iconWidth, alignment: .leading)
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

/// The sidebar's full-width dropdown: a `PopupMenuButton` on the chip
/// surface, with a caller-drawn label.
struct NavigatorDropdown<Label: View>: View {
    let entries: [PopupMenuEntry]
    @ViewBuilder let label: () -> Label

    var body: some View {
        PopupMenuButton(entries: entries) {
            label()
                .contentShape(RoundedRectangle(cornerRadius: Radius.chip, style: .continuous))
        }
        .navigatorChipSurface()
    }
}

/// A bordered rounded-square icon button, per the design's chip controls
/// (compose, New Project). The chip surface stays visible at rest.
struct NavigatorChipButton: View {
    let systemImage: String
    let help: String
    var isEnabled = true
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .frame(width: NavigatorMetrics.chipSize, height: NavigatorMetrics.chipSize)
                .contentShape(RoundedRectangle(cornerRadius: Radius.chip, style: .continuous))
        }
        .buttonStyle(.plain)
        .foregroundStyle(.secondary)
        .navigatorChipSurface()
        .opacity(isEnabled ? 1 : Dimming.unavailable)
        .disabled(!isEnabled)
        .help(help)
        .accessibilityLabel(help)
    }
}

struct NavigatorDisclosureHeader: View {
    let title: String
    @Binding var isExpanded: Bool
    let addHelp: String
    let isAddEnabled: Bool
    let onAdd: () -> Void

    @State private var isHovering = false

    var body: some View {
        HStack(spacing: Spacing.xs) {
            Button {
                isExpanded.toggle()
            } label: {
                HStack(spacing: Spacing.xs) {
                    Text(title)
                        .font(.headline)
                    if isHovering {
                        Image(systemName: isExpanded ? "chevron.down" : "chevron.right")
                            .font(.caption)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            // Persistent per the design: the add affordance is part of the
            // section header, not a hover reveal.
            NavigatorActionButton(
                systemImage: "plus",
                help: addHelp,
                isEnabled: isAddEnabled,
                action: onAdd
            )
        }
        .frame(minHeight: NavigatorMetrics.rowHeight)
        .foregroundStyle(.secondary)
        .navigatorSurface(isActive: isHovering)
        .contentShape(Rectangle())
        .onHover { isHovering = $0 }
        .navigatorListRow(top: Spacing.sm)
    }
}

extension View {
    func navigatorListRow(
        top: CGFloat = NavigatorMetrics.rowVerticalInset,
        bottom: CGFloat = NavigatorMetrics.rowVerticalInset
    ) -> some View {
        padding(.top, top)
            .padding(.bottom, bottom)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    func navigatorSurface(isActive: Bool) -> some View {
        padding(.horizontal, NavigatorMetrics.contentHorizontalPadding)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background {
                if isActive {
                    RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
                        .fill(.quaternary)
                }
            }
    }

    /// The bordered chip surface shared by the filter dropdown and chip
    /// buttons: a faint fill with a clearly visible stroke, present at rest.
    func navigatorChipSurface() -> some View {
        background {
            RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                .fill(Surface.chip)
        }
        .overlay {
            RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                .strokeBorder(Surface.chipBorder)
        }
    }
}
