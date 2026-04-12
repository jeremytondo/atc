import { type Static, Type } from "@sinclair/typebox";

export const TurnStatusSchema = Type.Union([
  Type.Object(
    {
      type: Type.Literal("inProgress"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("awaitingInput"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("completed"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("failed"),
      message: Type.Optional(Type.String({ minLength: 1 })),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("cancelled"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("interrupted"),
    },
    { additionalProperties: false },
  ),
]);
export type TurnStatus = Static<typeof TurnStatusSchema>;

export const TurnSchema = Type.Object(
  {
    id: Type.String({ minLength: 1 }),
    status: TurnStatusSchema,
  },
  { additionalProperties: false },
);
export type Turn = Static<typeof TurnSchema>;

const CommandExecutionItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
  Type.Literal("declined"),
]);

const PatchApplyItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
  Type.Literal("declined"),
]);

const ToolCallItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
]);

const CollabAgentToolSchema = Type.Union([
  Type.Literal("spawnAgent"),
  Type.Literal("sendInput"),
  Type.Literal("resumeAgent"),
  Type.Literal("wait"),
  Type.Literal("closeAgent"),
]);

const CollabAgentToolCallStatusSchema = ToolCallItemStatusSchema;

const DynamicToolCallStatusSchema = ToolCallItemStatusSchema;

const WebSearchActionSchema = Type.Union([
  Type.Object(
    {
      type: Type.Literal("search"),
      query: Type.Union([Type.String(), Type.Null()]),
      queries: Type.Union([Type.Array(Type.String()), Type.Null()]),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("openPage"),
      url: Type.Union([Type.String(), Type.Null()]),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("findInPage"),
      url: Type.Union([Type.String(), Type.Null()]),
      pattern: Type.Union([Type.String(), Type.Null()]),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("other"),
    },
    { additionalProperties: false },
  ),
]);

const UserMessageItemSchema = Type.Object(
  {
    type: Type.Literal("userMessage"),
    id: Type.String({ minLength: 1 }),
    content: Type.Array(Type.Unknown()),
  },
  { additionalProperties: false },
);

const AgentMessageItemSchema = Type.Object(
  {
    type: Type.Literal("agentMessage"),
    id: Type.String({ minLength: 1 }),
    text: Type.String(),
    phase: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: false },
);

const PlanItemSchema = Type.Object(
  {
    type: Type.Literal("plan"),
    id: Type.String({ minLength: 1 }),
    text: Type.String(),
  },
  { additionalProperties: false },
);

const ReasoningItemSchema = Type.Object(
  {
    type: Type.Literal("reasoning"),
    id: Type.String({ minLength: 1 }),
    summary: Type.Array(Type.String()),
    content: Type.Array(Type.String()),
  },
  { additionalProperties: false },
);

const CommandExecutionItemSchema = Type.Object(
  {
    type: Type.Literal("commandExecution"),
    id: Type.String({ minLength: 1 }),
    command: Type.String(),
    cwd: Type.String(),
    processId: Type.Union([Type.String(), Type.Null()]),
    status: CommandExecutionItemStatusSchema,
    commandActions: Type.Array(Type.Unknown()),
    aggregatedOutput: Type.Union([Type.String(), Type.Null()]),
    exitCode: Type.Union([Type.Integer(), Type.Null()]),
    durationMs: Type.Union([Type.Number(), Type.Null()]),
  },
  { additionalProperties: false },
);

const FileChangeItemSchema = Type.Object(
  {
    type: Type.Literal("fileChange"),
    id: Type.String({ minLength: 1 }),
    changes: Type.Array(Type.Unknown()),
    status: PatchApplyItemStatusSchema,
  },
  { additionalProperties: false },
);

const McpToolCallItemSchema = Type.Object(
  {
    type: Type.Literal("mcpToolCall"),
    id: Type.String({ minLength: 1 }),
    server: Type.String(),
    tool: Type.String(),
    status: ToolCallItemStatusSchema,
    arguments: Type.Unknown(),
    result: Type.Union([Type.Unknown(), Type.Null()]),
    error: Type.Union([Type.Unknown(), Type.Null()]),
    durationMs: Type.Union([Type.Number(), Type.Null()]),
  },
  { additionalProperties: false },
);

const DynamicToolCallItemSchema = Type.Object(
  {
    type: Type.Literal("dynamicToolCall"),
    id: Type.String({ minLength: 1 }),
    tool: Type.String(),
    arguments: Type.Unknown(),
    status: DynamicToolCallStatusSchema,
    contentItems: Type.Union([Type.Array(Type.Unknown()), Type.Null()]),
    success: Type.Union([Type.Boolean(), Type.Null()]),
    durationMs: Type.Union([Type.Number(), Type.Null()]),
  },
  { additionalProperties: false },
);

const CollabAgentToolCallItemSchema = Type.Object(
  {
    type: Type.Literal("collabAgentToolCall"),
    id: Type.String({ minLength: 1 }),
    tool: CollabAgentToolSchema,
    status: CollabAgentToolCallStatusSchema,
    senderThreadId: Type.String({ minLength: 1 }),
    receiverThreadIds: Type.Array(Type.String({ minLength: 1 })),
    prompt: Type.Union([Type.String(), Type.Null()]),
    agentsStates: Type.Record(Type.String(), Type.Unknown()),
  },
  { additionalProperties: false },
);

const WebSearchItemSchema = Type.Object(
  {
    type: Type.Literal("webSearch"),
    id: Type.String({ minLength: 1 }),
    query: Type.String(),
    action: Type.Union([WebSearchActionSchema, Type.Null()]),
  },
  { additionalProperties: false },
);

const ImageViewItemSchema = Type.Object(
  {
    type: Type.Literal("imageView"),
    id: Type.String({ minLength: 1 }),
    path: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);

const ImageGenerationItemSchema = Type.Object(
  {
    type: Type.Literal("imageGeneration"),
    id: Type.String({ minLength: 1 }),
    status: Type.String({ minLength: 1 }),
    revisedPrompt: Type.Union([Type.String(), Type.Null()]),
    result: Type.String(),
  },
  { additionalProperties: false },
);

const EnteredReviewModeItemSchema = Type.Object(
  {
    type: Type.Literal("enteredReviewMode"),
    id: Type.String({ minLength: 1 }),
    review: Type.String(),
  },
  { additionalProperties: false },
);

const ExitedReviewModeItemSchema = Type.Object(
  {
    type: Type.Literal("exitedReviewMode"),
    id: Type.String({ minLength: 1 }),
    review: Type.String(),
  },
  { additionalProperties: false },
);

const ContextCompactionItemSchema = Type.Object(
  {
    type: Type.Literal("contextCompaction"),
    id: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);

export const TurnItemSchema = Type.Union([
  UserMessageItemSchema,
  AgentMessageItemSchema,
  PlanItemSchema,
  ReasoningItemSchema,
  CommandExecutionItemSchema,
  FileChangeItemSchema,
  McpToolCallItemSchema,
  DynamicToolCallItemSchema,
  CollabAgentToolCallItemSchema,
  WebSearchItemSchema,
  ImageViewItemSchema,
  ImageGenerationItemSchema,
  EnteredReviewModeItemSchema,
  ExitedReviewModeItemSchema,
  ContextCompactionItemSchema,
]);
export type TurnItem = Static<typeof TurnItemSchema>;

export const TurnTerminalErrorSchema = Type.Object(
  {
    message: Type.String({ minLength: 1 }),
    providerError: Type.Union([Type.Unknown(), Type.Null()]),
    additionalDetails: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: false },
);
export type TurnTerminalError = Static<typeof TurnTerminalErrorSchema>;

export const TurnDetailSchema = Type.Object(
  {
    id: Type.String({ minLength: 1 }),
    status: TurnStatusSchema,
    items: Type.Array(TurnItemSchema),
    error: Type.Union([TurnTerminalErrorSchema, Type.Null()]),
  },
  { additionalProperties: false },
);
export type TurnDetail = Static<typeof TurnDetailSchema>;

export const TurnPlanStepSchema = Type.Object(
  {
    step: Type.String({ minLength: 1 }),
    status: Type.Union([
      Type.Literal("pending"),
      Type.Literal("in_progress"),
      Type.Literal("completed"),
    ]),
  },
  { additionalProperties: false },
);
export type TurnPlanStep = Static<typeof TurnPlanStepSchema>;

export const TurnDiffFileSummarySchema = Type.Object(
  {
    path: Type.String({ minLength: 1 }),
    additions: Type.Integer(),
    deletions: Type.Integer(),
  },
  { additionalProperties: false },
);
export type TurnDiffFileSummary = Static<typeof TurnDiffFileSummarySchema>;

export const TurnStartParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    prompt: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type TurnStartParams = Static<typeof TurnStartParamsSchema>;

export const TurnStartResultSchema = Type.Object(
  {
    turn: TurnSchema,
  },
  { additionalProperties: false },
);
export type TurnStartResult = Static<typeof TurnStartResultSchema>;

export const TurnStartedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turn: TurnSchema,
  },
  { additionalProperties: false },
);
export type TurnStartedNotificationParams = Static<typeof TurnStartedNotificationParamsSchema>;

export const TurnCompletedNotificationParamsSchema = TurnStartedNotificationParamsSchema;
export type TurnCompletedNotificationParams = Static<typeof TurnCompletedNotificationParamsSchema>;

export const TurnPlanUpdatedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    explanation: Type.Optional(Type.String()),
    steps: Type.Array(TurnPlanStepSchema),
  },
  { additionalProperties: false },
);
export type TurnPlanUpdatedNotificationParams = Static<
  typeof TurnPlanUpdatedNotificationParamsSchema
>;

export const TurnDiffUpdatedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    diff: Type.String(),
    summary: Type.Array(TurnDiffFileSummarySchema),
  },
  { additionalProperties: false },
);
export type TurnDiffUpdatedNotificationParams = Static<
  typeof TurnDiffUpdatedNotificationParamsSchema
>;

export const ItemStartedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    item: TurnItemSchema,
  },
  { additionalProperties: false },
);
export type ItemStartedNotificationParams = Static<typeof ItemStartedNotificationParamsSchema>;

export const ItemCompletedNotificationParamsSchema = ItemStartedNotificationParamsSchema;
export type ItemCompletedNotificationParams = Static<typeof ItemCompletedNotificationParamsSchema>;

const ItemTextDeltaNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    itemId: Type.String({ minLength: 1 }),
    delta: Type.String(),
  },
  { additionalProperties: false },
);

export const ItemMessageTextDeltaNotificationParamsSchema = ItemTextDeltaNotificationParamsSchema;
export type ItemMessageTextDeltaNotificationParams = Static<
  typeof ItemMessageTextDeltaNotificationParamsSchema
>;

export const ItemReasoningTextDeltaNotificationParamsSchema = ItemTextDeltaNotificationParamsSchema;
export type ItemReasoningTextDeltaNotificationParams = Static<
  typeof ItemReasoningTextDeltaNotificationParamsSchema
>;

export const ItemReasoningSummaryTextDeltaNotificationParamsSchema =
  ItemTextDeltaNotificationParamsSchema;
export type ItemReasoningSummaryTextDeltaNotificationParams = Static<
  typeof ItemReasoningSummaryTextDeltaNotificationParamsSchema
>;

export const ItemCommandExecutionOutputDeltaNotificationParamsSchema =
  ItemTextDeltaNotificationParamsSchema;
export type ItemCommandExecutionOutputDeltaNotificationParams = Static<
  typeof ItemCommandExecutionOutputDeltaNotificationParamsSchema
>;

export const ItemToolProgressNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    itemId: Type.String({ minLength: 1 }),
    message: Type.String(),
  },
  { additionalProperties: false },
);
export type ItemToolProgressNotificationParams = Static<
  typeof ItemToolProgressNotificationParamsSchema
>;
