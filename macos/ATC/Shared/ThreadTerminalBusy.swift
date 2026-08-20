/// openThreadTerminal's ThreadBusy refusal (ATC-203): the server is driving
/// a turn on a one-process provider and launches the TUI itself when the
/// turn ends — a wait, not a failure to alert on.
struct ThreadTerminalBusy: Error {}
