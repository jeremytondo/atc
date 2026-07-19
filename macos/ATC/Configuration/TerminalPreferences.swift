struct TerminalPreferences: Equatable, Sendable {
    let theme: String?
    let fontFamily: String?
    let fontSize: Float?
    let paddingX: Int?
    let paddingY: Int?
    let background: String?
    let backgroundOpacity: Double?

    init(
        theme: String? = nil,
        fontFamily: String? = nil,
        fontSize: Float? = nil,
        paddingX: Int? = nil,
        paddingY: Int? = nil,
        background: String? = nil,
        backgroundOpacity: Double? = nil
    ) {
        self.theme = theme
        self.fontFamily = fontFamily
        self.fontSize = fontSize
        self.paddingX = paddingX
        self.paddingY = paddingY
        self.background = background
        self.backgroundOpacity = backgroundOpacity
    }
}
