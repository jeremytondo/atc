import { type Static, type TSchema, Type } from "@sinclair/typebox";
import { Value } from "@sinclair/typebox/value";
import { createTurnItemSchemas } from "@/turns/item-schemas";

const CodexReasoningEffortSchema = Type.Union([
  Type.Literal("none"),
  Type.Literal("minimal"),
  Type.Literal("low"),
  Type.Literal("medium"),
  Type.Literal("high"),
  Type.Literal("xhigh"),
]);

const CodexSupportedReasoningEffortSchema = Type.Object(
  {
    reasoningEffort: CodexReasoningEffortSchema,
    description: Type.Optional(Type.String()),
  },
  { additionalProperties: true },
);

const CodexModelSchema = Type.Object(
  {
    id: Type.String(),
    model: Type.String(),
    displayName: Type.String(),
    hidden: Type.Boolean(),
    supportedReasoningEfforts: Type.Array(CodexSupportedReasoningEffortSchema),
    defaultReasoningEffort: Type.Optional(Type.Union([CodexReasoningEffortSchema, Type.Null()])),
    inputModalities: Type.Optional(Type.Array(Type.String())),
    supportsPersonality: Type.Optional(Type.Boolean()),
    isDefault: Type.Optional(Type.Boolean()),
  },
  { additionalProperties: true },
);

const CodexModelListResponseSchema = Type.Object(
  {
    data: Type.Array(CodexModelSchema),
    nextCursor: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: true },
);

const CodexThreadStatusSchema = Type.Union([
  Type.Object(
    {
      type: Type.Literal("notLoaded"),
    },
    { additionalProperties: true },
  ),
  Type.Object(
    {
      type: Type.Literal("idle"),
    },
    { additionalProperties: true },
  ),
  Type.Object(
    {
      type: Type.Literal("active"),
      activeFlags: Type.Array(Type.String()),
    },
    { additionalProperties: true },
  ),
  Type.Object(
    {
      type: Type.Literal("systemError"),
      error: Type.Optional(
        Type.Union([
          Type.Object(
            {
              message: Type.Optional(Type.String()),
            },
            { additionalProperties: true },
          ),
          Type.Null(),
        ]),
      ),
    },
    { additionalProperties: true },
  ),
]);

const permissiveTurnItemSchemas = createTurnItemSchemas({
  additionalProperties: true,
  stringSchema: Type.String(),
  nonEmptyStringSchema: Type.String(),
  exitCodeSchema: Type.Union([Type.Number(), Type.Null()]),
});

export const CodexThreadItemSchema = permissiveTurnItemSchemas.TurnItemSchema;

const CodexTurnErrorSchema = Type.Object(
  {
    message: Type.String(),
    codexErrorInfo: Type.Union([Type.Unknown(), Type.Null()]),
    additionalDetails: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: true },
);

export const CodexTurnDetailSchema = Type.Object(
  {
    id: Type.String(),
    status: Type.Union([
      Type.Literal("completed"),
      Type.Literal("interrupted"),
      Type.Literal("failed"),
      Type.Literal("inProgress"),
    ]),
    items: Type.Array(CodexThreadItemSchema),
    error: Type.Union([CodexTurnErrorSchema, Type.Null()]),
  },
  { additionalProperties: true },
);

const CodexThreadSchema = Type.Object(
  {
    id: Type.String(),
    preview: Type.String(),
    createdAt: Type.Number(),
    updatedAt: Type.Number(),
    name: Type.Union([Type.String(), Type.Null()]),
    status: CodexThreadStatusSchema,
    cwd: Type.String(),
    turns: Type.Optional(Type.Array(CodexTurnDetailSchema)),
  },
  { additionalProperties: true },
);

const CodexThreadListResponseSchema = Type.Object(
  {
    data: Type.Array(CodexThreadSchema),
    nextCursor: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: true },
);

const CodexThreadResponseSchema = Type.Object(
  {
    thread: CodexThreadSchema,
  },
  { additionalProperties: true },
);

const CodexEmptyResponseSchema = Type.Object({}, { additionalProperties: true });

const CodexConfiguredThreadResponseSchema = Type.Object(
  {
    thread: CodexThreadSchema,
    model: Type.String(),
    reasoningEffort: Type.Union([CodexReasoningEffortSchema, Type.Null()]),
  },
  { additionalProperties: true },
);

const CodexTurnSchema = Type.Object(
  {
    id: Type.String(),
    status: Type.Union([
      Type.Literal("completed"),
      Type.Literal("interrupted"),
      Type.Literal("failed"),
      Type.Literal("inProgress"),
    ]),
    error: Type.Optional(Type.Union([CodexTurnErrorSchema, Type.Null()])),
  },
  { additionalProperties: true },
);

const CodexTurnResponseSchema = Type.Object(
  {
    turn: CodexTurnSchema,
  },
  { additionalProperties: true },
);

const CodexTurnSteerResponseSchema = Type.Object(
  {
    turnId: Type.String(),
  },
  { additionalProperties: true },
);

const CodexInitializeResponseSchema = Type.Object(
  {
    userAgent: Type.Optional(Type.String()),
  },
  { additionalProperties: true },
);

const CodexTurnInterruptResponseSchema = Type.Object({}, { additionalProperties: true });

export const CodexTurnPlanUpdatedNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    explanation: Type.Optional(Type.String()),
    plan: Type.Array(
      Type.Object(
        {
          step: Type.String(),
          status: Type.String(),
        },
        { additionalProperties: true },
      ),
    ),
  },
  { additionalProperties: true },
);

export const CodexTurnDiffUpdatedNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    diff: Type.String(),
  },
  { additionalProperties: true },
);

export const CodexReasoningTextDeltaNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    delta: Type.String(),
    contentIndex: Type.Optional(Type.Integer()),
  },
  { additionalProperties: true },
);

export const CodexAgentMessageDeltaNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    delta: Type.String(),
  },
  { additionalProperties: true },
);

export const CodexReasoningSummaryTextDeltaNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    delta: Type.String(),
    summaryIndex: Type.Integer(),
  },
  { additionalProperties: true },
);

export const CodexCommandExecutionOutputDeltaNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    delta: Type.String(),
  },
  { additionalProperties: true },
);

export const CodexMcpToolCallProgressNotificationSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    message: Type.String(),
  },
  { additionalProperties: true },
);

export const CodexCommandExecutionRequestApprovalParamsSchema = Type.Object(
  {
    threadId: Type.String(),
    turnId: Type.String(),
    itemId: Type.String(),
    command: Type.Optional(Type.String()),
    cwd: Type.Optional(Type.String()),
    commandActions: Type.Optional(Type.Array(Type.Unknown())),
  },
  { additionalProperties: true },
);

export type CodexReasoningEffort = Static<typeof CodexReasoningEffortSchema>;
export type CodexModel = Static<typeof CodexModelSchema>;
export type CodexModelListResponse = Static<typeof CodexModelListResponseSchema>;
export type CodexThreadStatus = Static<typeof CodexThreadStatusSchema>;
export type CodexThread = Static<typeof CodexThreadSchema>;
export type CodexThreadItem = Static<typeof CodexThreadItemSchema>;
export type CodexThreadListResponse = Static<typeof CodexThreadListResponseSchema>;
export type CodexConfiguredThreadResponse = Static<typeof CodexConfiguredThreadResponseSchema>;
export type CodexTurn = Static<typeof CodexTurnSchema>;
export type CodexTurnDetail = Static<typeof CodexTurnDetailSchema>;
export type CodexTurnError = Static<typeof CodexTurnErrorSchema>;
export type CodexInitializeResponse = Static<typeof CodexInitializeResponseSchema>;
export type CodexTurnPlanUpdatedNotification = Static<
  typeof CodexTurnPlanUpdatedNotificationSchema
>;
export type CodexTurnDiffUpdatedNotification = Static<
  typeof CodexTurnDiffUpdatedNotificationSchema
>;
export type CodexReasoningTextDeltaNotification = Static<
  typeof CodexReasoningTextDeltaNotificationSchema
>;
export type CodexAgentMessageDeltaNotification = Static<
  typeof CodexAgentMessageDeltaNotificationSchema
>;
export type CodexReasoningSummaryTextDeltaNotification = Static<
  typeof CodexReasoningSummaryTextDeltaNotificationSchema
>;
export type CodexCommandExecutionOutputDeltaNotification = Static<
  typeof CodexCommandExecutionOutputDeltaNotificationSchema
>;
export type CodexMcpToolCallProgressNotification = Static<
  typeof CodexMcpToolCallProgressNotificationSchema
>;
export type CodexCommandExecutionRequestApprovalParams = Static<
  typeof CodexCommandExecutionRequestApprovalParamsSchema
>;

export type CodexSupportedReasoningEffort = Readonly<{
  reasoningEffort: CodexReasoningEffort;
  description?: string;
}>;

export type CodexInitializeParams = Readonly<{
  clientInfo: Readonly<{
    name: string;
    title: string | null;
    version: string;
  }>;
  capabilities: Readonly<{
    experimentalApi: boolean;
  }>;
}>;

export type CodexClientNotification = Readonly<{
  method: "initialized";
}>;

export type CodexAskForApproval = "untrusted" | "on-failure" | "on-request" | "never";

export type CodexModelListParams = Readonly<{
  limit?: number;
  includeHidden?: boolean;
}>;

export type CodexThreadListParams = Readonly<{
  cursor?: string;
  limit?: number;
  archived?: boolean;
  cwd?: string;
}>;

export type CodexThreadStartParams = Readonly<{
  cwd: string;
  model?: string;
  approvalPolicy?: CodexAskForApproval;
  sandbox?: undefined;
  experimentalRawEvents: boolean;
  persistExtendedHistory: boolean;
}>;

export type CodexThreadResumeParams = Readonly<{
  threadId: string;
  cwd: string;
  model?: string;
  approvalPolicy?: CodexAskForApproval;
  sandbox?: undefined;
  persistExtendedHistory: boolean;
}>;

export type CodexThreadReadParams = Readonly<{
  threadId: string;
  includeTurns?: boolean;
}>;

export type CodexThreadForkParams = Readonly<{
  threadId: string;
  cwd?: string;
  model?: string;
  persistExtendedHistory: boolean;
}>;

export type CodexThreadArchiveParams = Readonly<{
  threadId: string;
}>;

export type CodexThreadUnarchiveParams = Readonly<{
  threadId: string;
}>;

export type CodexThreadSetNameParams = Readonly<{
  threadId: string;
  name: string;
}>;

export type CodexUserInput = Readonly<{
  type: "text";
  text: string;
  text_elements: readonly [];
}>;

export type CodexTurnStartParams = Readonly<{
  threadId: string;
  input: readonly [CodexUserInput];
  cwd?: string;
  model?: string;
  effort?: CodexReasoningEffort;
}>;

export type CodexTurnSteerParams = Readonly<{
  threadId: string;
  expectedTurnId: string;
  input: readonly [CodexUserInput];
}>;

export type CodexTurnInterruptParams = Readonly<{
  threadId: string;
  turnId: string;
}>;

export type CodexCommandExecutionApprovalDecision =
  | "accept"
  | "acceptForSession"
  | "decline"
  | "cancel";

export type CodexFileChangeApprovalDecision = "accept" | "acceptForSession" | "decline" | "cancel";

export type CodexMcpServerElicitationRequestResponse = Readonly<{
  action: "accept" | "decline" | "cancel";
  content: null;
  _meta: null;
}>;

export const parseCodexInitializeResponse = (candidate: unknown): CodexInitializeResponse =>
  validateCodexPayload(CodexInitializeResponseSchema, candidate, "initialize response");

export const parseCodexModelListResponse = (candidate: unknown): CodexModelListResponse =>
  validateCodexPayload(CodexModelListResponseSchema, candidate, "model/list response");

export const parseCodexThreadListResponse = (candidate: unknown): CodexThreadListResponse =>
  validateCodexPayload(CodexThreadListResponseSchema, candidate, "thread/list response");

export const parseCodexThreadResponse = (candidate: unknown): { thread: CodexThread } =>
  validateCodexPayload(CodexThreadResponseSchema, candidate, "thread response");

export const parseCodexEmptyResponse = (candidate: unknown, label: string): Record<string, never> =>
  validateCodexPayload(CodexEmptyResponseSchema, candidate, label);

export const parseCodexConfiguredThreadResponse = (
  candidate: unknown,
): CodexConfiguredThreadResponse =>
  validateCodexPayload(
    CodexConfiguredThreadResponseSchema,
    candidate,
    "configured thread response",
  );

export const parseCodexTurnResponse = (candidate: unknown): { turn: CodexTurn } =>
  validateCodexPayload(CodexTurnResponseSchema, candidate, "turn response");

export const parseCodexTurnSteerResponse = (candidate: unknown): { turnId: string } =>
  validateCodexPayload(CodexTurnSteerResponseSchema, candidate, "turn/steer response");

export const parseCodexTurnInterruptResponse = (candidate: unknown): Record<string, unknown> =>
  validateCodexPayload(CodexTurnInterruptResponseSchema, candidate, "turn/interrupt response");

const validateCodexPayload = <TSchemaType extends TSchema>(
  schema: TSchemaType,
  candidate: unknown,
  label: string,
): Static<TSchemaType> => {
  if (!Value.Check(schema, candidate)) {
    const issues = [...Value.Errors(schema, candidate)].slice(0, 3).map((validationError) => {
      const path = validationError.path || "/";
      return `${path}: ${validationError.message}`;
    });
    const detail = issues.length > 0 ? ` ${issues.join("; ")}` : "";

    throw new Error(`Invalid Codex ${label}.${detail}`);
  }

  return candidate as Static<TSchemaType>;
};
