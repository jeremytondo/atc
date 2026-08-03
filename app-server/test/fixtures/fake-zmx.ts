import * as fs from "node:fs"
import * as path from "node:path"

// A deterministic stand-in for the zmx binary, driven by environment
// variables (baked into the per-test wrapper script) so the TerminalAdapter
// can be tested without a live zmx:
//
//   FAKE_ZMX_STATE          directory of session files (required)
//   FAKE_ZMX_LIST_MODE      "fail" exits 1
//   FAKE_ZMX_UNREACHABLE    a session name always listed as an err= entry
//                           (exists but does not answer the probe)
//   FAKE_ZMX_ATTACH_MODE    "exit" ends right after creating the session
//                           (a command that finished); "fail-fast" prints an
//                           error and exits 1 without creating anything
//   FAKE_ZMX_SETTLE_MS      delay before the created session becomes listed
//   FAKE_ZMX_KILL_MODE      "ignore" leaves the session alive
//   FAKE_ZMX_KILL_MS        delay between kill returning and the session
//                           leaving the inventory (zmx returns before death)
//
// Sessions are JSON files named after the session. A `diesAt` timestamp
// models delayed death: list omits sessions whose time has passed.

const stateDir = process.env["FAKE_ZMX_STATE"]
if (stateDir === undefined) {
  console.error("fake-zmx: FAKE_ZMX_STATE is not set")
  process.exit(2)
}
fs.mkdirSync(stateDir, { recursive: true })

const sessionFile = (name: string) => path.join(stateDir, name)

type SessionRecord = {
  name: string
  pid: number
  created: number
  startDir: string
  command: Array<string>
  env: Record<string, string | undefined>
  diesAt?: number
}

const readSessions = (): Array<SessionRecord> =>
  fs
    .readdirSync(stateDir)
    .map((entry) => JSON.parse(fs.readFileSync(sessionFile(entry), "utf8")) as SessionRecord)
    .filter((record) => {
      if (record.diesAt !== undefined && record.diesAt <= Date.now()) {
        fs.rmSync(sessionFile(record.name), { force: true })
        return false
      }
      return true
    })

const [command = "", name = "", ...rest] = process.argv.slice(2)

switch (command) {
  case "list": {
    if (process.env["FAKE_ZMX_LIST_MODE"] === "fail") {
      console.error("fake-zmx: inventory unavailable")
      process.exit(1)
    }
    const unreachable = process.env["FAKE_ZMX_UNREACHABLE"]
    if (unreachable !== undefined && unreachable !== "") {
      console.log(`  name=${unreachable}\terr=Timeout\tstatus=unreachable`)
    }
    for (const record of readSessions()) {
      if (record.name === unreachable) continue
      console.log(
        `  name=${record.name}\tpid=${record.pid}\tclients=0\tcreated=${record.created}\tstart_dir=${record.startDir}`,
      )
    }
    process.exit(0)
  }
  case "attach": {
    if (name === "") process.exit(2)
    const mode = process.env["FAKE_ZMX_ATTACH_MODE"]
    if (mode === "fail-fast") {
      console.error("fake-zmx: cannot create session")
      process.exit(1)
    }
    const settleMs = Number(process.env["FAKE_ZMX_SETTLE_MS"] ?? "0")
    await Bun.sleep(settleMs)
    // Like zmx: attaching to an existing session never re-creates it (the
    // command and cwd of a second attach are silently ignored).
    if (!fs.existsSync(sessionFile(name))) {
      const record: SessionRecord = {
        name,
        pid: process.pid,
        created: Math.floor(Date.now() / 1000),
        startDir: process.cwd(),
        command: rest,
        env: {
          ZMX_SESSION: process.env["ZMX_SESSION"],
          ZMX_SESSION_PREFIX: process.env["ZMX_SESSION_PREFIX"],
          ZMX_DIR: process.env["ZMX_DIR"],
          TERM: process.env["TERM"],
        },
      }
      fs.writeFileSync(sessionFile(name), JSON.stringify(record))
    }
    if (mode === "exit") process.exit(0)
    // Attached client: echo terminal input back until detached (killed).
    process.stdout.write("attached\n")
    const decoder = new TextDecoder()
    for await (const chunk of Bun.stdin.stream()) {
      process.stdout.write(`echo:${decoder.decode(chunk)}`)
    }
    break
  }
  case "kill": {
    if (name === "") process.exit(2)
    // Like zmx: exit codes prove nothing, and death happens after return.
    if (process.env["FAKE_ZMX_KILL_MODE"] !== "ignore" && fs.existsSync(sessionFile(name))) {
      const record = JSON.parse(fs.readFileSync(sessionFile(name), "utf8")) as SessionRecord
      record.diesAt = Date.now() + Number(process.env["FAKE_ZMX_KILL_MS"] ?? "0")
      fs.writeFileSync(sessionFile(name), JSON.stringify(record))
    }
    process.exit(0)
  }
  default: {
    console.error(`fake-zmx: unknown command "${command}"`)
    process.exit(2)
  }
}
