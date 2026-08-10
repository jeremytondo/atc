import { isAbsolute } from "node:path"
import { describe, expect, test } from "vitest"
import {
  launchAgentPath,
  launchAgentPlist,
  programArguments,
  serviceName,
  systemdUnit,
  systemdUnitPath,
} from "../../src/cli/service.ts"

// Unit-file generation is the test-covered core of `atc service` (the
// launchctl/systemctl calls stay thin and untested by design — they would
// touch the developer's real session).

describe("service unit generation", () => {
  test("launch agent plist wraps the serve command with restart and PATH", () => {
    const plist = launchAgentPlist(
      ["/usr/local/bin/atc", "serve"],
      "/home/u/state/service.log",
      "/opt/homebrew/bin:/usr/bin",
    )
    expect(plist).toContain(`<string>${serviceName}</string>`)
    expect(plist).toContain("<string>/usr/local/bin/atc</string>")
    expect(plist).toContain("<string>serve</string>")
    expect(plist).toContain("<key>RunAtLoad</key>")
    expect(plist).toContain("<key>KeepAlive</key>")
    expect(plist).toContain("<key>EnvironmentVariables</key>")
    expect(plist).toContain("<string>/opt/homebrew/bin:/usr/bin</string>")
    expect(plist).toContain("<string>/home/u/state/service.log</string>")
  })

  test("launch agent plist escapes XML metacharacters in paths", () => {
    const plist = launchAgentPlist(["/odd & <path>/atc", "serve"], "/log & file.log", "/p&q")
    expect(plist).toContain("<string>/odd &amp; &lt;path&gt;/atc</string>")
    expect(plist).toContain("<string>/log &amp; file.log</string>")
    expect(plist).toContain("<string>/p&amp;q</string>")
    expect(plist).not.toContain("/odd & <path>")
  })

  test("systemd unit quotes ExecStart, stamps PATH, and restarts with backoff", () => {
    const unit = systemdUnit(
      ["/path with space/atc", "serve"],
      "/home/u/state/service.log",
      "/opt/bin:/usr/bin",
    )
    expect(unit).toContain('ExecStart="/path with space/atc" "serve"')
    expect(unit).toContain('Environment="PATH=/opt/bin:/usr/bin"')
    expect(unit).toContain("Restart=always")
    expect(unit).toContain("RestartSec=5")
    expect(unit).toContain("StandardOutput=append:/home/u/state/service.log")
    expect(unit).toContain("StandardError=append:/home/u/state/service.log")
    expect(unit).toContain("WantedBy=default.target")
  })

  test("systemd unit escapes percent and backslash (specifier expansion)", () => {
    const unit = systemdUnit(["/od%d\\path/atc", "serve"], "/log.log", "/bin")
    expect(unit).toContain('ExecStart="/od%%d\\\\path/atc" "serve"')
  })

  test("unit paths follow each platform's convention", () => {
    expect(launchAgentPath("/Users/u")).toBe(`/Users/u/Library/LaunchAgents/${serviceName}.plist`)
    expect(systemdUnitPath({ HOME: "/home/u" })).toBe(
      `/home/u/.config/systemd/user/${serviceName}.service`,
    )
    expect(systemdUnitPath({ XDG_CONFIG_HOME: "/xdg", HOME: "/home/u" })).toBe(
      `/xdg/systemd/user/${serviceName}.service`,
    )
  })

  test("program arguments: compiled binary alone, dev via the bun runtime", () => {
    expect(programArguments(false)).toEqual([process.execPath, "serve"])
    const dev = programArguments(true)
    expect(dev[0]).toBe(process.execPath)
    expect(dev).toHaveLength(3)
    expect(isAbsolute(dev[1]!)).toBe(true)
    expect(dev[2]).toBe("serve")
  })
})
