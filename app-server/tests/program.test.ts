import { expect, test } from "bun:test";
import { createProgram, DEFAULT_DEV_PORT } from "../src/cli/program.ts";

const buildInfo = {
  version: "1.2.3",
  commit: "abc1234",
  builtAt: "2026-01-01T00:00:00Z",
};

// exitOverride/configureOutput are per-command in Commander, so apply them to
// the subcommands as well: tests must observe errors, not exit the process.
function captureServe() {
  const calls: { port: number }[] = [];
  const program = createProgram({
    buildInfo,
    serve: async (options) => {
      calls.push(options);
    },
  });
  const silent = { writeOut: () => {}, writeErr: () => {} };
  for (const command of [program, ...program.commands]) {
    command.exitOverride().configureOutput(silent);
  }
  return { program, calls };
}

test("serve uses the development default port", async () => {
  const { program, calls } = captureServe();
  await program.parseAsync(["serve"], { from: "user" });
  expect(calls).toEqual([{ port: DEFAULT_DEV_PORT }]);
});

test("serve --port passes the parsed port through", async () => {
  const { program, calls } = captureServe();
  await program.parseAsync(["serve", "--port", "0"], { from: "user" });
  expect(calls).toEqual([{ port: 0 }]);
});

const validPorts = ["0", "8080", "65535"];
const invalidPorts = [
  "",
  " 8080 ",
  "banana",
  "1.5",
  "-1",
  "65536",
  "0x50",
  "1e4",
  "+80",
];

test.each(validPorts)("serve --port accepts %j", async (value) => {
  const { program, calls } = captureServe();
  await program.parseAsync(["serve", "--port", value], { from: "user" });
  expect(calls).toEqual([{ port: Number(value) }]);
});

test.each(invalidPorts)(
  "serve --port rejects %j without calling serve",
  async (value) => {
    const { program, calls } = captureServe();
    await expect(
      program.parseAsync(["serve", "--port", value], { from: "user" }),
    ).rejects.toThrow("port must be an integer");
    expect(calls).toEqual([]);
  },
);

test("--version reports the injected application version", async () => {
  const { program } = captureServe();
  await expect(
    program.parseAsync(["--version"], { from: "user" }),
  ).rejects.toThrow("1.2.3");
});
