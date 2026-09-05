package receiver

// archDeniedSyscalls: arm64 creates processes only through clone and
// clone3, which the shared list already covers.
var archDeniedSyscalls []uint32
