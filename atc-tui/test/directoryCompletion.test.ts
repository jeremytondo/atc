import { describe, expect, it } from "vitest"
import type * as AppServer from "../src/appServer.ts"
import {
  completedInput,
  displayPath,
  listingPath,
  resolveInput,
  suggestions,
} from "../src/directoryCompletion.ts"

const home = "/srv/users/jeremy"

const listing: AppServer.DirectoryListing = {
  path: home,
  parent: "/srv/users",
  entries: [
    { name: "ATC", path: home + "/ATC" },
    { name: "projects", path: home + "/projects" },
    { name: "Source", path: home + "/Source" },
  ],
}

describe("directory completion", () => {
  it("resolves only absolute paths and the current server user's home shorthand", () => {
    expect(resolveInput("~", home)).toBe(home)
    expect(resolveInput("~/projects/../ATC/", home)).toBe(home + "/ATC")
    expect(resolveInput("/opt/work/../atc", home)).toBe("/opt/atc")
    expect(resolveInput("relative/path", home)).toBeUndefined()
    expect(resolveInput("~someone/project", home)).toBeUndefined()
    expect(resolveInput("~/project", undefined)).toBeUndefined()
  })

  it("chooses the parent to list without consulting the client filesystem", () => {
    expect(listingPath("", home)).toBe(home)
    expect(listingPath("~", home)).toBe(home)
    expect(listingPath("~/Pro", home)).toBe(home)
    expect(listingPath("~/projects/", home)).toBe(home + "/projects")
    expect(listingPath("/opt/atc", home)).toBe("/opt")
  })

  it("keeps paths under server home concise while preserving path boundaries", () => {
    expect(displayPath(home, home)).toBe("~")
    expect(displayPath(home + "/projects", home)).toBe("~/projects")
    expect(displayPath("/srv/users/jeremy-old", home)).toBe("/srv/users/jeremy-old")
    expect(completedInput(home + "/projects", home)).toBe("~/projects/")
    expect(completedInput("/", home)).toBe("/")
  })

  it("filters immediate children case-insensitively and preserves server casing", () => {
    expect(suggestions("~/s", home, listing)).toEqual([{ name: "Source", path: home + "/Source" }])
    expect(suggestions("~/PROJECT", home, listing)).toEqual([
      { name: "projects", path: home + "/projects" },
    ])
    expect(suggestions("~/missing", home, listing)).toEqual([])
    expect(suggestions("", home, listing)).toEqual(listing.entries)
    expect(suggestions("~", home, listing)).toEqual(listing.entries)
    expect(suggestions("~/", home, listing)).toEqual(listing.entries)
  })
})
