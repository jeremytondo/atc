import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createWriteStream, type WriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { createInterface, type Interface } from "node:readline";

export type JsonObject = Record<string, unknown>;

export interface ProtocolMessage extends JsonObject {
  id?: number | string;
  method?: string;
  params?: JsonObject;
  result?: JsonObject;
  error?: {
    code?: number;
    message?: string;
    data?: unknown;
  };
}

interface PendingRequest {
  method: string;
  resolve: (result: JsonObject) => void;
  reject: (error: Error) => void;
  timer: NodeJS.Timeout;
}

interface MessageWaiter {
  predicate: (message: ProtocolMessage) => boolean;
  resolve: (message: ProtocolMessage) => void;
  reject: (error: Error) => void;
  timer: NodeJS.Timeout;
}

export interface AppServerClientOptions {
  cwd: string;
  rawLogPath: string;
  requestTimeoutMs?: number;
}

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;

export class AppServerClient {
  readonly rawLogPath: string;

  private readonly child: ChildProcessWithoutNullStreams;
  private readonly output: Interface;
  private readonly errors: Interface;
  private readonly rawLog: WriteStream;
  private readonly requestTimeoutMs: number;
  private readonly pending = new Map<number, PendingRequest>();
  private readonly waiters = new Set<MessageWaiter>();
  private readonly receivedMessages: ProtocolMessage[] = [];
  private readonly exitPromise: Promise<number | null>;
  private nextRequestId = 1;
  private eventSequence = 0;
  private stopped = false;

  private constructor(
    options: AppServerClientOptions,
    child: ChildProcessWithoutNullStreams,
    rawLog: WriteStream,
  ) {
    this.rawLogPath = options.rawLogPath;
    this.requestTimeoutMs =
      options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
    this.child = child;
    this.rawLog = rawLog;
    this.output = createInterface({ input: child.stdout });
    this.errors = createInterface({ input: child.stderr });
    this.exitPromise = new Promise((resolve) => {
      child.once("exit", (code) => resolve(code));
    });

    this.output.on("line", (line) => this.handleServerLine(line));
    this.errors.on("line", (line) => {
      console.error(`[app-server stderr] ${line}`);
    });
    child.once("error", (error) => this.failAll(error));
    child.once("exit", (code, signal) => {
      if (!this.stopped && (code !== 0 || signal !== null)) {
        this.failAll(
          new Error(
            `codex app-server exited unexpectedly (code=${String(code)}, signal=${String(signal)})`,
          ),
        );
      }
    });
  }

  static async start(
    options: AppServerClientOptions,
  ): Promise<AppServerClient> {
    await mkdir(dirname(options.rawLogPath), { recursive: true });
    const rawLog = createWriteStream(options.rawLogPath, {
      encoding: "utf8",
      flags: "wx",
    });

    await new Promise<void>((resolve, reject) => {
      rawLog.once("open", () => resolve());
      rawLog.once("error", reject);
    });

    const child = spawn("codex", ["app-server", "--stdio"], {
      cwd: options.cwd,
      env: process.env,
      stdio: ["pipe", "pipe", "pipe"],
    });

    const client = new AppServerClient(options, child, rawLog);
    try {
      await client.initialize();
      return client;
    } catch (error) {
      await client.stop();
      throw error;
    }
  }

  async request(method: string, params: JsonObject = {}): Promise<JsonObject> {
    this.ensureRunning();
    const id = this.nextRequestId++;
    console.log(`→ request #${id} ${method}`);

    return await new Promise<JsonObject>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(
          new Error(
            `Timed out after ${this.requestTimeoutMs}ms waiting for ${method} response`,
          ),
        );
      }, this.requestTimeoutMs);

      this.pending.set(id, { method, resolve, reject, timer });
      this.send({ id, method, params });
    });
  }

  notify(method: string, params: JsonObject = {}): void {
    this.ensureRunning();
    console.log(`→ notification ${method}`);
    this.send({ method, params });
  }

  async waitForNotification(
    method: string,
    predicate: (params: JsonObject) => boolean = () => true,
    timeoutMs = this.requestTimeoutMs,
  ): Promise<ProtocolMessage> {
    this.ensureRunning();

    return await new Promise<ProtocolMessage>((resolve, reject) => {
      const waiter: MessageWaiter = {
        predicate: (message) =>
          message.method === method && predicate(message.params ?? {}),
        resolve,
        reject,
        timer: setTimeout(() => {
          this.waiters.delete(waiter);
          reject(
            new Error(
              `Timed out after ${timeoutMs}ms waiting for ${method} notification`,
            ),
          );
        }, timeoutMs),
      };
      this.waiters.add(waiter);
    });
  }

  get messageCount(): number {
    return this.receivedMessages.length;
  }

  messagesSince(index: number): ProtocolMessage[] {
    return this.receivedMessages.slice(index);
  }

  async stop(): Promise<void> {
    if (this.stopped) {
      return;
    }
    this.stopped = true;
    this.child.stdin.end();

    const exited = await Promise.race([
      this.exitPromise.then(() => true),
      new Promise<false>((resolve) =>
        setTimeout(() => resolve(false), 2_000),
      ),
    ]);
    if (!exited) {
      this.child.kill("SIGTERM");
      await this.exitPromise;
    }

    this.output.close();
    this.errors.close();
    this.failAll(new Error("codex app-server client stopped"));
    await new Promise<void>((resolve, reject) => {
      this.rawLog.once("error", reject);
      this.rawLog.end(resolve);
    });
  }

  private async initialize(): Promise<void> {
    await this.request("initialize", {
      clientInfo: {
        name: "atc_provider_identity_resume_poc",
        title: "ATC Provider Identity and Resume POC",
        version: "0.1.0",
      },
      capabilities: null,
    });
    this.notify("initialized");
  }

  private handleServerLine(line: string): void {
    this.rawLog.write(`${line}\n`);
    this.eventSequence += 1;

    let message: ProtocolMessage;
    try {
      const parsed: unknown = JSON.parse(line);
      if (!isObject(parsed)) {
        throw new Error("top-level value is not an object");
      }
      message = parsed;
    } catch (error) {
      const failure = new Error(
        `Invalid JSON from codex app-server at event ${this.eventSequence}: ${String(error)}`,
      );
      console.error(`← event ${this.eventSequence} invalid JSON`);
      this.failAll(failure);
      return;
    }

    this.receivedMessages.push(message);
    this.printReadableMessage(message);

    if (message.id !== undefined && message.method === undefined) {
      this.resolveResponse(message);
    } else if (message.id !== undefined && message.method !== undefined) {
      this.rejectServerRequest(message);
    }

    for (const waiter of this.waiters) {
      if (waiter.predicate(message)) {
        clearTimeout(waiter.timer);
        this.waiters.delete(waiter);
        waiter.resolve(message);
      }
    }
  }

  private resolveResponse(message: ProtocolMessage): void {
    if (typeof message.id !== "number") {
      return;
    }
    const pending = this.pending.get(message.id);
    if (pending === undefined) {
      return;
    }

    clearTimeout(pending.timer);
    this.pending.delete(message.id);
    if (message.error !== undefined) {
      pending.reject(
        new Error(
          `${pending.method} failed (${String(message.error.code ?? "unknown")}): ${message.error.message ?? "unknown error"}`,
        ),
      );
      return;
    }
    pending.resolve(message.result ?? {});
  }

  private rejectServerRequest(message: ProtocolMessage): void {
    console.log(
      `→ rejecting server request ${message.method ?? "unknown"} (safe POC default)`,
    );
    this.send({
      id: message.id,
      error: {
        code: -32601,
        message: "ATC read-only POC does not handle server requests",
      },
    });
  }

  private printReadableMessage(message: ProtocolMessage): void {
    const prefix = `← event ${String(this.eventSequence).padStart(3, "0")}`;
    if (message.method !== undefined) {
      const params = message.params ?? {};
      const thread = objectAt(params, "thread");
      const turn = objectAt(params, "turn");
      const item = objectAt(params, "item");

      if (message.method === "thread/started") {
        console.log(
          `${prefix} thread/started id=${stringAt(thread, "id") ?? "unknown"} cwd=${stringAt(thread, "cwd") ?? "unknown"}`,
        );
        return;
      }
      if (message.method === "turn/started") {
        console.log(
          `${prefix} turn/started id=${stringAt(turn, "id") ?? "unknown"}`,
        );
        return;
      }
      if (message.method === "turn/completed") {
        console.log(
          `${prefix} turn/completed id=${stringAt(turn, "id") ?? "unknown"} status=${stringAt(turn, "status") ?? "unknown"}`,
        );
        return;
      }
      if (
        message.method === "item/completed" &&
        stringAt(item, "type") === "agentMessage"
      ) {
        console.log(
          `${prefix} agent: ${JSON.stringify(stringAt(item, "text") ?? "")}`,
        );
        return;
      }
      if (
        message.method === "item/agentMessage/delta" &&
        typeof params.delta === "string"
      ) {
        console.log(`${prefix} agent delta: ${JSON.stringify(params.delta)}`);
        return;
      }
      if (item !== undefined) {
        console.log(
          `${prefix} ${message.method} item=${stringAt(item, "type") ?? "unknown"}`,
        );
        return;
      }
      console.log(`${prefix} ${message.method}`);
      return;
    }

    if (message.error !== undefined) {
      console.log(
        `${prefix} response #${String(message.id)} error=${message.error.message ?? "unknown"}`,
      );
    } else {
      console.log(`${prefix} response #${String(message.id)} ok`);
    }
  }

  private send(message: ProtocolMessage): void {
    this.child.stdin.write(`${JSON.stringify(message)}\n`);
  }

  private ensureRunning(): void {
    if (this.stopped || this.child.exitCode !== null) {
      throw new Error("codex app-server client is not running");
    }
  }

  private failAll(error: Error): void {
    for (const request of this.pending.values()) {
      clearTimeout(request.timer);
      request.reject(error);
    }
    this.pending.clear();

    for (const waiter of this.waiters) {
      clearTimeout(waiter.timer);
      waiter.reject(error);
    }
    this.waiters.clear();
  }
}

export function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function objectAt(
  object: JsonObject | undefined,
  key: string,
): JsonObject | undefined {
  const value = object?.[key];
  return isObject(value) ? value : undefined;
}

export function stringAt(
  object: JsonObject | undefined,
  key: string,
): string | undefined {
  const value = object?.[key];
  return typeof value === "string" ? value : undefined;
}
