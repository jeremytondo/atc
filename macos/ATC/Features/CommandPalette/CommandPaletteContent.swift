struct CommandPaletteRow: Identifiable {
    let id: CommandID
    let title: String
    let matchedRanges: [Range<String.Index>]
    let shortcut: KeyStroke?
    let availability: CommandAvailability
}

@MainActor
enum CommandPaletteContent {
    static func rows(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [CommandPaletteRow] {
        CommandRegistry.allDescriptors.compactMap { descriptor in
            guard descriptor.isPaletteEligible,
                  let match = QueryMatcher.match(query, in: descriptor.title)
            else { return nil }
            return CommandPaletteRow(
                id: descriptor.id,
                title: descriptor.title,
                matchedRanges: match.ranges,
                shortcut: keymap.menuShortcuts[descriptor.id],
                availability: descriptor.availability(context)
            )
        }.sorted { isOrderedBefore(
            title: $0.title, id: $0.id,
            thanTitle: $1.title, id: $1.id
        ) }
    }

    static func isOrderedBefore(
        title lhsTitle: String,
        id lhsID: CommandID,
        thanTitle rhsTitle: String,
        id rhsID: CommandID
    ) -> Bool {
        let lhs = lhsTitle.lowercased()
        let rhs = rhsTitle.lowercased()
        return lhs == rhs ? lhsID.rawValue < rhsID.rawValue : lhs < rhs
    }
}
