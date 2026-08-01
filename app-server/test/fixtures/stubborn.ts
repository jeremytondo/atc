// Fixture child: ignores SIGTERM so cleanup must escalate to SIGKILL.
process.on("SIGTERM", () => {})
setInterval(() => {}, 1_000)
console.log("armed")
