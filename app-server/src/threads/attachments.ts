import { Context, Effect, FileSystem, Layer, Stream } from "effect"
import { join } from "node:path"
import {
  ATTACHMENT_MAX_BYTES,
  ATTACHMENT_MEDIA_TYPES,
  AttachmentInvalid,
  AttachmentNotFound,
  AttachmentTooLarge,
  ThreadArchived,
  ThreadNotFound,
} from "../api/contract.ts"
import type * as Contract from "../api/contract.ts"
import { AppConfig } from "../platform/config.ts"
import { AttachmentRepository } from "./attachmentRepository.ts"
import type { AttachmentRecord } from "./attachmentRepository.ts"
import { ThreadRepository } from "./threadRepository.ts"

// Thread attachments (ATC-216): the images a prompt carries. The first
// per-thread state on disk — bytes live at
// `<dataDir>/attachments/<threadId>/<attachmentId>.<ext>`, metadata in
// thread_attachments (attachmentRepository.ts). Invariants:
//
//   - An attachment belongs to exactly one thread and is reachable only
//     through it: `resolve` never finds another thread's id, and the blob
//     directory is keyed by thread so `purge` (Threads.delete) removes every
//     byte the thread ever held. Nothing else deletes blobs — there is no
//     orphan collection, by design: clients upload at send time.
//   - The stored media type is the truth of the bytes, not of the header:
//     an upload whose bytes lack the declared format's signature is refused
//     (AttachmentInvalid), so `path` can be handed to any consumer as the
//     image it claims to be.
//   - `path` is stable for the attachment's life and is what the adapters
//     hand a provider that reads local files (Codex localImage); the Claude
//     adapter reads the bytes itself.

type MediaType = (typeof ATTACHMENT_MEDIA_TYPES)[number]
type ThreadAttachment = typeof Contract.ThreadAttachment.Type

const EXTENSION: Record<MediaType, string> = {
  "image/png": "png",
  "image/jpeg": "jpg",
  "image/gif": "gif",
  "image/webp": "webp",
}

const isMediaType = (value: string): value is MediaType =>
  (ATTACHMENT_MEDIA_TYPES as ReadonlyArray<string>).includes(value)

/** Whether `bytes` opens with `mediaType`'s file signature. */
const carriesSignature = (bytes: Uint8Array, mediaType: MediaType): boolean => {
  const startsWith = (offset: number, expected: ReadonlyArray<number>): boolean =>
    expected.every((byte, index) => bytes[offset + index] === byte)
  switch (mediaType) {
    case "image/png":
      return startsWith(0, [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    case "image/jpeg":
      return startsWith(0, [0xff, 0xd8, 0xff])
    case "image/gif":
      // "GIF8"
      return startsWith(0, [0x47, 0x49, 0x46, 0x38])
    case "image/webp":
      // "RIFF" <size> "WEBP"
      return startsWith(0, [0x52, 0x49, 0x46, 0x46]) && startsWith(8, [0x57, 0x45, 0x42, 0x50])
  }
}

const isControlCharacter = (character: string): boolean => {
  const code = character.codePointAt(0) ?? 0
  return code < 0x20 || code === 0x7f
}

/** A display name safe to store and show: one path component, no control
 * characters, bounded; the type's default when nothing usable remains. */
const sanitizeName = (name: string | undefined, mediaType: MediaType): string => {
  const cleaned = [...(name ?? "").split(/[\\/]/).at(-1)!]
    .filter((character) => !isControlCharacter(character))
    .join("")
    .trim()
    .slice(0, 120)
  return cleaned === "" || cleaned === "." || cleaned === ".."
    ? `image.${EXTENSION[mediaType]}`
    : cleaned
}

export class Attachments extends Context.Service<
  Attachments,
  {
    /**
     * Store one uploaded image for the thread. `mediaType` is the request's
     * Content-Type (parameters allowed, stripped here); the bytes must carry
     * that format's signature and fit the cap.
     */
    readonly create: (
      threadId: string,
      input: {
        readonly bytes: Uint8Array
        readonly mediaType: string
        readonly name?: string | undefined
      },
    ) => Effect.Effect<
      ThreadAttachment,
      ThreadNotFound | ThreadArchived | AttachmentTooLarge | AttachmentInvalid
    >
    /** The stored bytes of one of the thread's attachments. */
    readonly stream: (
      threadId: string,
      attachmentId: string,
    ) => Effect.Effect<Stream.Stream<Uint8Array>, ThreadNotFound | AttachmentNotFound>
    /**
     * The thread's attachments for `ids`, in that order. Every id must be
     * one of the thread's own — the first that is not fails the call.
     */
    readonly resolve: (
      threadId: string,
      ids: ReadonlyArray<string>,
    ) => Effect.Effect<ReadonlyArray<ThreadAttachment>, AttachmentNotFound>
    /** Remove the thread's blob directory (its rows cascade with the thread).
     * Best-effort: a failure is logged, never surfaced. */
    readonly purge: (threadId: string) => Effect.Effect<void>
  }
>()("app-server/Attachments") {}

export const layer = Layer.effect(Attachments)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const repository = yield* AttachmentRepository
    const threads = yield* ThreadRepository

    const root = join(config.dataDir, "attachments")
    const directory = (threadId: string) => join(root, threadId)

    // A stored media type is always one of ours (the write boundary
    // validates); anything else is corruption, and a defect.
    const asMediaType = (stored: string): MediaType => {
      if (isMediaType(stored)) return stored
      throw new Error(`attachment row carries unknown media type ${stored}`)
    }

    const pathOf = (record: AttachmentRecord) =>
      join(directory(record.threadId), `${record.id}.${EXTENSION[asMediaType(record.mediaType)]}`)

    const toAttachment = (record: AttachmentRecord): ThreadAttachment => ({
      id: record.id,
      name: record.name,
      mediaType: asMediaType(record.mediaType),
      byteSize: record.byteSize,
      path: pathOf(record),
      createdAt: record.createdAt,
    })

    const resolve: Attachments["Service"]["resolve"] = (threadId, ids) =>
      repository.findMany(threadId, ids).pipe(
        Effect.flatMap((records) => {
          const found = new Set(records.map((record) => record.id))
          const missing = ids.find((id) => !found.has(id))
          return missing === undefined
            ? Effect.succeed(records.map(toAttachment))
            : Effect.fail(new AttachmentNotFound({ threadId, attachmentId: missing }))
        }),
      )

    return {
      create: (threadId, input) =>
        Effect.gen(function* () {
          const thread = yield* threads.require(threadId)
          if (thread.archivedAt !== undefined) {
            return yield* Effect.fail(new ThreadArchived({ threadId }))
          }
          const mediaType = input.mediaType.split(";")[0]!.trim().toLowerCase()
          if (!isMediaType(mediaType)) {
            return yield* Effect.fail(
              new AttachmentInvalid({ threadId, reason: `unsupported image type ${mediaType}` }),
            )
          }
          if (input.bytes.byteLength === 0) {
            return yield* Effect.fail(
              new AttachmentInvalid({ threadId, reason: "the body is empty" }),
            )
          }
          if (input.bytes.byteLength > ATTACHMENT_MAX_BYTES) {
            return yield* Effect.fail(
              new AttachmentTooLarge({
                threadId,
                byteSize: input.bytes.byteLength,
                limit: ATTACHMENT_MAX_BYTES,
              }),
            )
          }
          if (!carriesSignature(input.bytes, mediaType)) {
            return yield* Effect.fail(
              new AttachmentInvalid({
                threadId,
                reason: `the bytes are not ${mediaType} (no ${EXTENSION[mediaType]} signature)`,
              }),
            )
          }
          const record = yield* repository.create({
            threadId,
            name: sanitizeName(input.name, mediaType),
            mediaType,
            byteSize: input.bytes.byteLength,
          })
          // The row first (its foreign key proves the thread exists) so the
          // path is known; a write that fails is a platform defect (disk
          // full, permissions). A delete that cascaded the row while the
          // bytes were landing already purged the directory, so the write
          // is undone here rather than left as an orphan.
          yield* fs
            .makeDirectory(directory(threadId), { recursive: true })
            .pipe(Effect.andThen(fs.writeFile(pathOf(record), input.bytes)), Effect.orDie)
          const still = yield* repository.findMany(threadId, [record.id])
          if (still.length === 0) {
            yield* fs
              .remove(directory(threadId), { recursive: true, force: true })
              .pipe(Effect.orDie)
            return yield* Effect.fail(new ThreadNotFound({ threadId }))
          }
          return toAttachment(record)
        }),
      stream: (threadId, attachmentId) =>
        Effect.gen(function* () {
          yield* threads.require(threadId)
          const [attachment] = yield* resolve(threadId, [attachmentId])
          // The row exists, so the file does too (create wrote it; only
          // purge removes it, with the rows): a read failure is a defect.
          return fs.stream(attachment!.path).pipe(Stream.orDie)
        }),
      resolve,
      purge: (threadId) =>
        fs
          .remove(directory(threadId), { recursive: true, force: true })
          .pipe(
            Effect.catch((error) =>
              Effect.logWarning("the thread's attachment directory could not be removed").pipe(
                Effect.annotateLogs({ threadId, reason: error.message }),
              ),
            ),
          ),
    } satisfies Attachments["Service"]
  }),
)
