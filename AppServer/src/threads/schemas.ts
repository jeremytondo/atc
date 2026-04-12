import { type Static, Type } from "@sinclair/typebox";
import { ModelReasoningEffortSchema } from "@/agents/schemas";
import { TurnDetailSchema } from "@/turns/schemas";

export const ThreadExecutionStatusSchema = Type.Union([
  Type.Object(
    {
      type: Type.Literal("notLoaded"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("idle"),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("active"),
      activeFlags: Type.Array(Type.String({ minLength: 1 })),
    },
    { additionalProperties: false },
  ),
  Type.Object(
    {
      type: Type.Literal("systemError"),
      message: Type.Optional(Type.String({ minLength: 1 })),
    },
    { additionalProperties: false },
  ),
]);
export type ThreadExecutionStatus = Static<typeof ThreadExecutionStatusSchema>;

export const ThreadSchema = Type.Object(
  {
    id: Type.String({ minLength: 1 }),
    preview: Type.String(),
    createdAt: Type.String({ minLength: 1 }),
    updatedAt: Type.String({ minLength: 1 }),
    name: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
    archived: Type.Boolean(),
    model: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
    reasoningEffort: Type.Union([ModelReasoningEffortSchema, Type.Null()]),
    status: ThreadExecutionStatusSchema,
  },
  { additionalProperties: false },
);
export type Thread = Static<typeof ThreadSchema>;

export const ThreadDetailSchema = Type.Object(
  {
    id: Type.String({ minLength: 1 }),
    preview: Type.String(),
    createdAt: Type.String({ minLength: 1 }),
    updatedAt: Type.String({ minLength: 1 }),
    name: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
    archived: Type.Boolean(),
    model: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
    reasoningEffort: Type.Union([ModelReasoningEffortSchema, Type.Null()]),
    status: ThreadExecutionStatusSchema,
    turns: Type.Array(TurnDetailSchema),
  },
  { additionalProperties: false },
);
export type ThreadDetail = Static<typeof ThreadDetailSchema>;

export const ThreadListParamsSchema = Type.Object(
  {
    cursor: Type.Optional(Type.String({ minLength: 1 })),
    limit: Type.Optional(Type.Integer({ minimum: 1 })),
    archived: Type.Optional(Type.Boolean()),
  },
  { additionalProperties: false },
);
export type ThreadListParams = Static<typeof ThreadListParamsSchema>;

export const ThreadListResultSchema = Type.Object(
  {
    threads: Type.Array(ThreadSchema),
    nextCursor: Type.Union([Type.String({ minLength: 1 }), Type.Null()]),
  },
  { additionalProperties: false },
);
export type ThreadListResult = Static<typeof ThreadListResultSchema>;

export const ThreadReadParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    includeTurns: Type.Optional(Type.Boolean()),
  },
  { additionalProperties: false },
);
export type ThreadReadParams = Static<typeof ThreadReadParamsSchema>;

export const ThreadStartParamsSchema = Type.Object(
  {
    model: Type.Optional(Type.String({ minLength: 1 })),
    reasoningEffort: Type.Optional(ModelReasoningEffortSchema),
  },
  { additionalProperties: false },
);
export type ThreadStartParams = Static<typeof ThreadStartParamsSchema>;

export const ThreadReadResultSchema = Type.Object(
  {
    thread: ThreadDetailSchema,
  },
  { additionalProperties: false },
);
export type ThreadReadResult = Static<typeof ThreadReadResultSchema>;

export const ThreadStartResultSchema = Type.Object(
  {
    thread: ThreadSchema,
  },
  { additionalProperties: false },
);
export type ThreadStartResult = Static<typeof ThreadStartResultSchema>;

export const ThreadResumeParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    model: Type.Optional(Type.String({ minLength: 1 })),
    reasoningEffort: Type.Optional(ModelReasoningEffortSchema),
  },
  { additionalProperties: false },
);
export type ThreadResumeParams = Static<typeof ThreadResumeParamsSchema>;

export const ThreadResumeResultSchema = ThreadStartResultSchema;
export type ThreadResumeResult = ThreadStartResult;

export const ThreadForkParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    model: Type.Optional(Type.String({ minLength: 1 })),
  },
  { additionalProperties: false },
);
export type ThreadForkParams = Static<typeof ThreadForkParamsSchema>;

export const ThreadForkResultSchema = ThreadStartResultSchema;
export type ThreadForkResult = ThreadStartResult;

export const ThreadArchiveParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadArchiveParams = Static<typeof ThreadArchiveParamsSchema>;

export const ThreadArchiveResultSchema = Type.Object({}, { additionalProperties: false });
export type ThreadArchiveResult = Static<typeof ThreadArchiveResultSchema>;

export const ThreadUnarchiveParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadUnarchiveParams = Static<typeof ThreadUnarchiveParamsSchema>;

export const ThreadUnarchiveResultSchema = ThreadStartResultSchema;
export type ThreadUnarchiveResult = ThreadStartResult;

export const ThreadSetNameParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    name: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadSetNameParams = Static<typeof ThreadSetNameParamsSchema>;

export const ThreadSetNameResultSchema = Type.Object({}, { additionalProperties: false });
export type ThreadSetNameResult = Static<typeof ThreadSetNameResultSchema>;

export const ThreadStartedNotificationParamsSchema = Type.Object(
  {
    thread: ThreadSchema,
  },
  { additionalProperties: false },
);
export type ThreadStartedNotificationParams = Static<typeof ThreadStartedNotificationParamsSchema>;

export const ThreadStatusChangedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    status: ThreadExecutionStatusSchema,
  },
  { additionalProperties: false },
);
export type ThreadStatusChangedNotificationParams = Static<
  typeof ThreadStatusChangedNotificationParamsSchema
>;

export const ThreadClosedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadClosedNotificationParams = Static<typeof ThreadClosedNotificationParamsSchema>;

export const ThreadArchivedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadArchivedNotificationParams = Static<
  typeof ThreadArchivedNotificationParamsSchema
>;

export const ThreadUnarchivedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
  },
  { additionalProperties: false },
);
export type ThreadUnarchivedNotificationParams = Static<
  typeof ThreadUnarchivedNotificationParamsSchema
>;

export const ThreadNameUpdatedNotificationParamsSchema = Type.Object(
  {
    threadId: Type.String({ minLength: 1 }),
    threadName: Type.Optional(Type.Union([Type.String({ minLength: 1 }), Type.Null()])),
  },
  { additionalProperties: false },
);
export type ThreadNameUpdatedNotificationParams = Static<
  typeof ThreadNameUpdatedNotificationParamsSchema
>;
