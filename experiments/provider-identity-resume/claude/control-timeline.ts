import type {
  PermissionResult,
  SDKControlInterruptResponse,
  SDKMessage,
} from "@anthropic-ai/claude-agent-sdk";

export type ClaudeControlTimelineEvent =
  | {
      sequence: number;
      observedAt: string;
      source: "sdk";
      event: "message";
      messageSequence: number;
      type: string;
      subtype?: string;
      state?: string;
    }
  | {
      sequence: number;
      observedAt: string;
      source: "callback";
      event: "request";
      requestId: string;
      toolUseId: string;
      toolName: string;
      input: Record<string, unknown>;
    }
  | {
      sequence: number;
      observedAt: string;
      source: "callback";
      event: "response";
      requestId: string;
      toolUseId: string;
      toolName: string;
      result: PermissionResult;
    }
  | {
      sequence: number;
      observedAt: string;
      source: "client";
      event: "interrupt_request";
    }
  | {
      sequence: number;
      observedAt: string;
      source: "client";
      event: "interrupt_response";
      receipt?: SDKControlInterruptResponse;
    };

export class ClaudeControlTimeline {
  readonly events: ClaudeControlTimelineEvent[] = [];

  recordMessage(messageSequence: number, message: SDKMessage): void {
    this.events.push({
      ...this.baseEvent(),
      source: "sdk",
      event: "message",
      messageSequence,
      type: message.type,
      subtype: subtypeOf(message),
      state: stateOf(message),
    });
  }

  recordRequest(request: {
    requestId: string;
    toolUseId: string;
    toolName: string;
    input: Record<string, unknown>;
  }): void {
    this.events.push({
      ...this.baseEvent(),
      source: "callback",
      event: "request",
      ...request,
    });
  }

  recordResponse(response: {
    requestId: string;
    toolUseId: string;
    toolName: string;
    result: PermissionResult;
  }): void {
    this.events.push({
      ...this.baseEvent(),
      source: "callback",
      event: "response",
      ...response,
    });
  }

  recordInterruptRequest(): void {
    this.events.push({
      ...this.baseEvent(),
      source: "client",
      event: "interrupt_request",
    });
  }

  recordInterruptResponse(
    receipt: SDKControlInterruptResponse | undefined,
  ): void {
    this.events.push({
      ...this.baseEvent(),
      source: "client",
      event: "interrupt_response",
      receipt,
    });
  }

  toJsonLines(): string {
    return `${this.events.map((event) => JSON.stringify(event)).join("\n")}\n`;
  }

  private baseEvent(): Pick<
    ClaudeControlTimelineEvent,
    "sequence" | "observedAt"
  > {
    return {
      sequence: this.events.length + 1,
      observedAt: new Date().toISOString(),
    };
  }
}

function subtypeOf(message: SDKMessage): string | undefined {
  return "subtype" in message && typeof message.subtype === "string"
    ? message.subtype
    : undefined;
}

function stateOf(message: SDKMessage): string | undefined {
  return (
      message.type === "system" &&
        message.subtype === "session_state_changed"
    )
    ? message.state
    : undefined;
}
