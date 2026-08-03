// PTY fixture: reports whether it runs under a real terminal, echoes input,
// reports window-size changes, and exits on command — the deterministic
// counterpart for the Subprocess PTY seam tests.

console.log(`tty:${process.stdout.isTTY === true}`)

process.on("SIGWINCH", () => {
  console.log(`resize:${process.stdout.columns}x${process.stdout.rows}`)
})

const decoder = new TextDecoder()
for await (const chunk of Bun.stdin.stream()) {
  const text = decoder.decode(chunk)
  if (text.includes("q")) process.exit(7)
  process.stdout.write(`in:${text.trim()}\n`)
}
