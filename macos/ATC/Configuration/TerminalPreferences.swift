struct TerminalPreferences: Equatable, Sendable {
    let theme: String?
    let fontFamily: String?
    let fontSize: Float?
    let paddingX: Int?
    let paddingY: Int?

    init(
        theme: String? = nil,
        fontFamily: String? = nil,
        fontSize: Float? = nil,
        paddingX: Int? = nil,
        paddingY: Int? = nil
    ) {
        self.theme = theme
        self.fontFamily = fontFamily
        self.fontSize = fontSize
        self.paddingX = paddingX
        self.paddingY = paddingY
    }
}
