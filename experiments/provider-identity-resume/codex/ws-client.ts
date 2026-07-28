import { createWriteStream, type WriteStream } from "node:fs";
import { mkdir } from "node:fs/promises";
import { dirname } from "node:path";

import {
  isObject,
  type AppServerRpc,
  type JsonObject,
  type ProtocolMessage,
} from "./app-server-client.ts";

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

export interface WsAppServerClientOptions {
  url: string;
  clientName: string;
  rawLogPath: string;
  requestTimeoutMs?: number;
}

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;

/**
 * WebSocket client for `codex app-server --listen ws://...` — the same
 * JSON-RPC protocol as stdio, one JSON message per text frame. Server
 * requests are rejected with the same safe POC default as the stdio client.
 */
export class WsAppServerClient implements AppServerRpc {
  private readonly socket: WebSocket;
  private readonly rawLog: WriteStream;
  private readonly requestTimeoutMs: number;
  private readonly clientName: string;
  private readonly pending = new Map<number, PendingRequest>();
  private readonly waiters = new Set<MessageWaiter>();
  private readonly receivedMessages: ProtocolMessage[] = [];
  private nextRequestId = 1;
  private eventSequence = 0;
  private stopped = false;

  private constructor(
    options: WsAppServerClientOptions,
    socket: WebSocket,
    rawLog: WriteStream,
  ) {
    this.socket = socket;
    this.rawLog = rawLog;
    this.clientName = options.clientName;
    this.requestTimeoutMs =
      options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;

    socket.onmessage = (event) => {
      this.handleFrame(String(event.data));
    };
    socket.onclose = () => {
      if (!this.stopped) {
        this.failAll(new Error("app-server WebSocket closed unexpectedly"));
      }
    };
    socket.onerror = () => {
      if (!this.stopped) {
        this.failAll(new Error("app-server WebSocket errored"));
      }
    };
  }

  static async connect(
    options: WsAppServerClientOptions,
  ): Promise<WsAppServerClient> {
    await mkdir(dirname(options.rawLogPath), { recursive: true });
    const rawLog = createWriteStream(options.rawLogPath, {
      encoding: "utf8",
      flags: "wx",
    });
    await new Promise<void>((resolve, reject) => {
      rawLog.once("open", () => resolve());
      rawLog.once("error", reject);
    });

    const socket = new WebSocket(options.url);
    await new Promise<void>((resolve, reject) => {
      socket.onopen = () => resolve();
      socket.onerror = () =>
        reject(new Error(`Could not connect to ${options.url}`));
    });

    const client = new WsAppServerClient(options, socket, rawLog);
    await client.request("initialize", {
      clientInfo: {
        name: options.clientName,
        title: "ATC held-connection probe",
        version: "0.1.0",
      },
    });
    client.notify("initialized");
    return client;
  }

  async request(method: string, params: JsonObject = {}): Promise<JsonObject> {
    this.ensureRunning();
    const id = this.nextRequestId++;
    console.log(`→ [${this.clientName}] request #${id} ${method}`);

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
    this.socket.close();
    this.failAll(new Error("ws app-server client stopped"));
    await new Promise<void>((resolve, reject) => {
      this.rawLog.once("error", reject);
      this.rawLog.end(resolve);
    });
  }

  private handleFrame(frame: string): void {
    this.rawLog.write(`${frame}\n`);
    this.eventSequence += 1;

    let message: ProtocolMessage;
    try {
      const parsed: unknown = JSON.parse(frame);
      if (!isObject(parsed)) {
        throw new Error("top-level value is not an object");
      }
      message = parsed;
    } catch (error) {
      this.failAll(
        new Error(
          `Invalid JSON frame at event ${this.eventSequence}: ${String(error)}`,
        ),
      );
      return;
    }

    this.receivedMessages.push(message);
    const label =
      message.method ??
      (message.error === undefined
        ? `response #${String(message.id)}`
        : `error #${String(message.id)}`);
    console.log(
      `← [${this.clientName}] event ${String(this.eventSequence).padStart(3, "0")} ${label}`,
    );

    if (message.id !== undefined && message.method === undefined) {
      this.resolveResponse(message);
    } else if (message.id !== undefined && message.method !== undefined) {
      console.log(
        `→ [${this.clientName}] rejecting server request ${message.method} (safe POC default)`,
      );
      this.send({
        id: message.id,
        error: {
          code: -32601,
          message: "ATC read-only POC does not handle server requests",
        },
      });
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

  private send(message: ProtocolMessage): void {
    this.socket.send(JSON.stringify(message));
  }

  private ensureRunning(): void {
    if (this.stopped || this.socket.readyState !== WebSocket.OPEN) {
      throw new Error("ws app-server client is not running");
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
