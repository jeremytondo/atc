import { type Static, Type } from "@sinclair/typebox";
import { RequestIdSchema } from "@/core/protocol";

export const ApprovalDecisionResolutionSchema = Type.Union([
  Type.Literal("approved"),
  Type.Literal("approvedForSession"),
  Type.Literal("declined"),
  Type.Literal("cancelled"),
]);
export type ApprovalDecisionResolution = Static<typeof ApprovalDecisionResolutionSchema>;

export const ApprovalResolutionSchema = Type.Union([
  ApprovalDecisionResolutionSchema,
  Type.Literal("stale"),
]);
export type ApprovalResolution = Static<typeof ApprovalResolutionSchema>;

const ApprovalRequestBase = {
  requestId: RequestIdSchema,
  threadId: Type.String({ minLength: 1 }),
  turnId: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
  itemId: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
  supportedResolutions: Type.Array(ApprovalDecisionResolutionSchema),
} as const;

// Provider-defined command action entries are forwarded transparently until the
// App Server adopts a stable provider-neutral action contract.
const CommandActionSchema = Type.Unknown();

// MCP elicitation payloads can carry arbitrary JSON Schema fragments and
// provider metadata at this boundary, so they remain intentionally opaque.
const ApprovalRequestedSchemaPayloadSchema = Type.Unknown();
const ApprovalMetadataSchema = Type.Unknown();

export const ApprovalCommandExecutionRequestSchema = Type.Object(
  {
    ...ApprovalRequestBase,
    kind: Type.Literal("commandExecution"),
    approvalId: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
    reason: Type.Union([Type.String(), Type.Null()]),
    command: Type.Union([Type.String(), Type.Null()]),
    cwd: Type.Union([Type.String(), Type.Null()]),
    commandActions: Type.Union([Type.Array(CommandActionSchema), Type.Null()]),
  },
  { additionalProperties: false },
);
export type ApprovalCommandExecutionRequest = Static<typeof ApprovalCommandExecutionRequestSchema>;

export const ApprovalFileChangeRequestSchema = Type.Object(
  {
    ...ApprovalRequestBase,
    kind: Type.Literal("fileChange"),
    reason: Type.Union([Type.String(), Type.Null()]),
    grantRoot: Type.Union([Type.String(), Type.Null()]),
  },
  { additionalProperties: false },
);
export type ApprovalFileChangeRequest = Static<typeof ApprovalFileChangeRequestSchema>;

export const ApprovalMcpElicitationRequestSchema = Type.Object(
  {
    ...ApprovalRequestBase,
    kind: Type.Literal("mcpElicitation"),
    serverName: Type.String({ minLength: 1 }),
    mode: Type.Union([Type.Literal("form"), Type.Literal("url")]),
    message: Type.String(),
    requestedSchema: Type.Union([ApprovalRequestedSchemaPayloadSchema, Type.Null()]),
    url: Type.Union([Type.String(), Type.Null()]),
    elicitationId: Type.Union([Type.String(), Type.Null()]),
    metadata: Type.Union([ApprovalMetadataSchema, Type.Null()]),
  },
  { additionalProperties: false },
);
export type ApprovalMcpElicitationRequest = Static<typeof ApprovalMcpElicitationRequestSchema>;

export const ApprovalUnknownRequestSchema = Type.Object(
  {
    ...ApprovalRequestBase,
    kind: Type.Literal("unknown"),
  },
  { additionalProperties: false },
);
export type ApprovalUnknownRequest = Static<typeof ApprovalUnknownRequestSchema>;

export const ApprovalRequestSchema = Type.Union([
  ApprovalCommandExecutionRequestSchema,
  ApprovalFileChangeRequestSchema,
  ApprovalMcpElicitationRequestSchema,
  ApprovalUnknownRequestSchema,
]);
export type ApprovalRequest = Static<typeof ApprovalRequestSchema>;

export const ApprovalResolveParamsSchema = Type.Object(
  {
    requestId: RequestIdSchema,
    resolution: ApprovalDecisionResolutionSchema,
  },
  { additionalProperties: false },
);
export type ApprovalResolveParams = Static<typeof ApprovalResolveParamsSchema>;

export const ApprovalResolveResultSchema = Type.Object(
  {
    requestId: RequestIdSchema,
    resolution: ApprovalDecisionResolutionSchema,
  },
  { additionalProperties: false },
);
export type ApprovalResolveResult = Static<typeof ApprovalResolveResultSchema>;

export const ApprovalRequestedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    approval: ApprovalRequestSchema,
  },
  { additionalProperties: false },
);
export type ApprovalRequestedNotificationParams = Static<
  typeof ApprovalRequestedNotificationParamsSchema
>;

export const ApprovalResolvedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    approval: ApprovalRequestSchema,
    resolution: ApprovalResolutionSchema,
  },
  { additionalProperties: false },
);
export type ApprovalResolvedNotificationParams = Static<
  typeof ApprovalResolvedNotificationParamsSchema
>;
