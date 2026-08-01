// Fixture child: floods stderr to exercise the bounded diagnostics tail;
// the final line is oversized to exercise per-line truncation.
for (let i = 1; i <= 499; i++) console.error(`line ${i}`)
console.error(`end ${"x".repeat(2500)}`)
