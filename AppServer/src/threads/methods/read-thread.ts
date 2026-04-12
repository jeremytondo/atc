import { createThreadWorkspaceMismatchResult } from "@/core/protocol/errors";
import { assertNever, err, ok } from "@/core/shared";
import type { ThreadMethodDependencies } from "@/threads/methods/method-dependencies";
import { getThreadDefaults, mapPublicThreadDetail } from "@/threads/response-mapper";
import type { ThreadsService } from "@/threads/service-types";
import {
  createInvalidProviderPayloadServiceError,
  threadBelongsToWorkspace,
} from "@/threads/validation";

export const createReadThreadMethod =
  (context: ThreadMethodDependencies): ThreadsService["readThread"] =>
  async (requestId, workspace, params) => {
    const sessionResult = await context.registry.getSession();

    if (!sessionResult.ok) {
      if (sessionResult.error.type === "sessionUnavailable") {
        return err(sessionResult.error);
      }

      throw new Error(sessionResult.error.message);
    }

    const normalizedWorkspacePath = await context.normalizePath(workspace.workspacePath);
    const existingLink = await context.store.getWorkspaceThreadLink({
      workspaceId: workspace.id,
      provider: sessionResult.data.provider,
      threadId: params.threadId,
    });
    const readResult = await sessionResult.data.readThread(requestId, {
      threadId: params.threadId,
      includeTurns: params.includeTurns ?? false,
    });

    if (!readResult.ok) {
      switch (readResult.error.type) {
        case "sessionUnavailable":
        case "remoteError":
          return err(readResult.error);
        case "invalidProviderMessage":
          return err(createInvalidProviderPayloadServiceError("thread/read", readResult.error));
        default:
          return assertNever(readResult.error, "Unhandled thread/read error");
      }
    }

    if (
      !(await threadBelongsToWorkspace(
        readResult.data.thread,
        normalizedWorkspacePath,
        context.normalizePath,
      ))
    ) {
      return createThreadWorkspaceMismatchResult(
        readResult.data.thread.id,
        workspace.workspacePath,
        readResult.data.thread.workspacePath,
      );
    }

    await context.store.upsertWorkspaceThreadLinks({
      workspaceId: workspace.id,
      provider: sessionResult.data.provider,
      seenAt: context.now(),
      links: [
        Object.freeze({
          threadId: readResult.data.thread.id,
          threadWorkspacePath: readResult.data.thread.workspacePath,
          archived: readResult.data.thread.archived,
        }),
      ],
    });

    return ok({
      thread: mapPublicThreadDetail(readResult.data.thread, getThreadDefaults(existingLink)),
    });
  };
