package receiver

// archDeniedSyscalls: arm64 has none of the legacy syscalls; processes
// come only from clone and clone3 and metadata changes only through the
// *at variants, which the shared list already covers.
var archDeniedSyscalls []uint32
