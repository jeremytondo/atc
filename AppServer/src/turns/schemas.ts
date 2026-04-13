import { type Static, Type } from "@sinclair/typebox";
import { createTurnItemSchemas } from "@/turns/item-schemas";

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

const strictTurnItemSchemas = createTurnItemSchemas({
  additionalProperties: false,
  stringSchema: Type.String(),
  nonEmptyStringSchema: Type.String({ minLength: 1 }),
  exitCodeSchema: Type.Union([Type.Integer(), Type.Null()]),
});

export const TurnItemSchema = strictTurnItemSchemas.TurnItemSchema;
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

export const TurnSteerParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
    prompt: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type TurnSteerParams = Static<typeof TurnSteerParamsSchema>;

export const TurnSteerResultSchema = TurnStartResultSchema;
export type TurnSteerResult = Static<typeof TurnSteerResultSchema>;

export const TurnInterruptParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    turnId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type TurnInterruptParams = Static<typeof TurnInterruptParamsSchema>;

export const TurnInterruptResultSchema = TurnStartResultSchema;
export type TurnInterruptResult = Static<typeof TurnInterruptResultSchema>;

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
