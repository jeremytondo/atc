import type { TSchema } from "@sinclair/typebox";
import { Type } from "@sinclair/typebox";

export const TurnCommandExecutionItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
  Type.Literal("declined"),
]);

export const TurnPatchApplyItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
  Type.Literal("declined"),
]);

export const TurnToolCallItemStatusSchema = Type.Union([
  Type.Literal("inProgress"),
  Type.Literal("completed"),
  Type.Literal("failed"),
]);

export const TurnCollabAgentToolSchema = Type.Union([
  Type.Literal("spawnAgent"),
  Type.Literal("sendInput"),
  Type.Literal("resumeAgent"),
  Type.Literal("wait"),
  Type.Literal("closeAgent"),
]);

export const createTurnItemSchemas = <
  TStringSchema extends TSchema,
  TNonEmptyStringSchema extends TSchema,
  TExitCodeSchema extends TSchema,
>(
  options: Readonly<{
    additionalProperties: boolean;
    stringSchema: TStringSchema;
    nonEmptyStringSchema: TNonEmptyStringSchema;
    exitCodeSchema: TExitCodeSchema;
  }>,
) => {
  const objectOptions = {
    additionalProperties: options.additionalProperties,
  } as const;

  const WebSearchActionSchema = Type.Union([
    Type.Object(
      {
        type: Type.Literal("search"),
        query: Type.Union([options.stringSchema, Type.Null()]),
        queries: Type.Union([Type.Array(options.stringSchema), Type.Null()]),
      },
      objectOptions,
    ),
    Type.Object(
      {
        type: Type.Literal("openPage"),
        url: Type.Union([options.stringSchema, Type.Null()]),
      },
      objectOptions,
    ),
    Type.Object(
      {
        type: Type.Literal("findInPage"),
        url: Type.Union([options.stringSchema, Type.Null()]),
        pattern: Type.Union([options.stringSchema, Type.Null()]),
      },
      objectOptions,
    ),
    Type.Object(
      {
        type: Type.Literal("other"),
      },
      objectOptions,
    ),
  ]);

  const UserMessageItemSchema = Type.Object(
    {
      type: Type.Literal("userMessage"),
      id: options.nonEmptyStringSchema,
      content: Type.Array(Type.Unknown()),
    },
    objectOptions,
  );

  const AgentMessageItemSchema = Type.Object(
    {
      type: Type.Literal("agentMessage"),
      id: options.nonEmptyStringSchema,
      text: options.stringSchema,
      phase: Type.Union([options.stringSchema, Type.Null()]),
    },
    objectOptions,
  );

  const PlanItemSchema = Type.Object(
    {
      type: Type.Literal("plan"),
      id: options.nonEmptyStringSchema,
      text: options.stringSchema,
    },
    objectOptions,
  );

  const ReasoningItemSchema = Type.Object(
    {
      type: Type.Literal("reasoning"),
      id: options.nonEmptyStringSchema,
      summary: Type.Array(options.stringSchema),
      content: Type.Array(options.stringSchema),
    },
    objectOptions,
  );

  const CommandExecutionItemSchema = Type.Object(
    {
      type: Type.Literal("commandExecution"),
      id: options.nonEmptyStringSchema,
      command: options.stringSchema,
      cwd: options.stringSchema,
      processId: Type.Union([options.stringSchema, Type.Null()]),
      status: TurnCommandExecutionItemStatusSchema,
      commandActions: Type.Array(Type.Unknown()),
      aggregatedOutput: Type.Union([options.stringSchema, Type.Null()]),
      exitCode: options.exitCodeSchema,
      durationMs: Type.Union([Type.Number(), Type.Null()]),
    },
    objectOptions,
  );

  const FileChangeItemSchema = Type.Object(
    {
      type: Type.Literal("fileChange"),
      id: options.nonEmptyStringSchema,
      changes: Type.Array(Type.Unknown()),
      status: TurnPatchApplyItemStatusSchema,
    },
    objectOptions,
  );

  const McpToolCallItemSchema = Type.Object(
    {
      type: Type.Literal("mcpToolCall"),
      id: options.nonEmptyStringSchema,
      server: options.stringSchema,
      tool: options.stringSchema,
      status: TurnToolCallItemStatusSchema,
      arguments: Type.Unknown(),
      result: Type.Union([Type.Unknown(), Type.Null()]),
      error: Type.Union([Type.Unknown(), Type.Null()]),
      durationMs: Type.Union([Type.Number(), Type.Null()]),
    },
    objectOptions,
  );

  const DynamicToolCallItemSchema = Type.Object(
    {
      type: Type.Literal("dynamicToolCall"),
      id: options.nonEmptyStringSchema,
      tool: options.stringSchema,
      arguments: Type.Unknown(),
      status: TurnToolCallItemStatusSchema,
      contentItems: Type.Union([Type.Array(Type.Unknown()), Type.Null()]),
      success: Type.Union([Type.Boolean(), Type.Null()]),
      durationMs: Type.Union([Type.Number(), Type.Null()]),
    },
    objectOptions,
  );

  const CollabAgentToolCallItemSchema = Type.Object(
    {
      type: Type.Literal("collabAgentToolCall"),
      id: options.nonEmptyStringSchema,
      tool: TurnCollabAgentToolSchema,
      status: TurnToolCallItemStatusSchema,
      senderThreadId: options.nonEmptyStringSchema,
      receiverThreadIds: Type.Array(options.nonEmptyStringSchema),
      prompt: Type.Union([options.stringSchema, Type.Null()]),
      agentsStates: Type.Record(Type.String(), Type.Unknown()),
    },
    objectOptions,
  );

  const WebSearchItemSchema = Type.Object(
    {
      type: Type.Literal("webSearch"),
      id: options.nonEmptyStringSchema,
      query: options.stringSchema,
      action: Type.Union([WebSearchActionSchema, Type.Null()]),
    },
    objectOptions,
  );

  const ImageViewItemSchema = Type.Object(
    {
      type: Type.Literal("imageView"),
      id: options.nonEmptyStringSchema,
      path: options.nonEmptyStringSchema,
    },
    objectOptions,
  );

  const ImageGenerationItemSchema = Type.Object(
    {
      type: Type.Literal("imageGeneration"),
      id: options.nonEmptyStringSchema,
      status: options.nonEmptyStringSchema,
      revisedPrompt: Type.Union([options.stringSchema, Type.Null()]),
      result: options.stringSchema,
    },
    objectOptions,
  );

  const EnteredReviewModeItemSchema = Type.Object(
    {
      type: Type.Literal("enteredReviewMode"),
      id: options.nonEmptyStringSchema,
      review: options.stringSchema,
    },
    objectOptions,
  );

  const ExitedReviewModeItemSchema = Type.Object(
    {
      type: Type.Literal("exitedReviewMode"),
      id: options.nonEmptyStringSchema,
      review: options.stringSchema,
    },
    objectOptions,
  );

  const ContextCompactionItemSchema = Type.Object(
    {
      type: Type.Literal("contextCompaction"),
      id: options.nonEmptyStringSchema,
    },
    objectOptions,
  );

  const TurnItemSchema = Type.Union([
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

  return Object.freeze({
    TurnItemSchema,
  });
};
