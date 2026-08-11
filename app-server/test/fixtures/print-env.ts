// Prints every ATC_SUBPROCESS_TEST_* environment variable as KEY=VALUE, one
// per line — the fixture behind the env-composition subprocess test.
for (const [key, value] of Object.entries(process.env)) {
  if (key.startsWith("ATC_SUBPROCESS_TEST_")) console.log(`${key}=${value}`)
}
