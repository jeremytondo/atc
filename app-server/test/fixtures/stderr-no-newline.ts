// Fixture child: floods stderr with a single 2 MB write containing no
// newline, to prove the diagnostics tail stays bounded regardless.
process.stderr.write(`start ${"x".repeat(2_000_000)}`)
