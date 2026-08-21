// The two calls a prompt is made of, shared by the Chat model (an existing
// thread) and the New Thread composer (a thread created a moment earlier):
// upload one image, then prompt with the uploaded ids. Wire errors become
// `ServerError`s; callers decide what a failure means for their draft.

import ATCAppServerAPI
import Foundation
import OpenAPIRuntime

public enum ThreadPrompting {
    /// Uploads one image to the thread and returns its server record.
    private static func upload(
        _ attachment: PendingAttachment, to threadID: String, client: any APIProtocol
    ) async throws -> Components.Schemas.ThreadAttachment {
        let body = HTTPBody(attachment.data)
        let payload: Operations.CreateThreadAttachment.Input.Body =
            switch attachment.mediaType {
            case .imagePng: .png(body)
            case .imageJpeg: .jpeg(body)
            case .imageGif: .imageGif(body)
            case .imageWebp: .imageWebp(body)
            }
        switch try await client.createThreadAttachment(
            path: .init(threadId: threadID), query: .init(name: attachment.name), body: payload)
        {
        case .ok(let ok): return try ok.body.json
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .conflict(let failure): throw ServerError(try failure.body.json)
        case .contentTooLarge(let failure): throw ServerError(try failure.body.json)
        case .unprocessableContent(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }

    /// Uploads the images in order, then prompts the thread with their ids;
    /// `when` is the server's choice between queueing and joining the
    /// running turn (nil: the server's default).
    public static func prompt(
        _ text: String, attachments: [PendingAttachment], when: PromptWhen?,
        threadID: String, client: any APIProtocol
    ) async throws -> Components.Schemas.PromptThreadResponse {
        var ids: [String] = []
        for attachment in attachments {
            ids.append(try await upload(attachment, to: threadID, client: client).id)
        }
        let request = Components.Schemas.PromptThreadRequest(
            prompt: text, attachments: ids.isEmpty ? nil : ids, when: when)
        switch try await client.promptThread(path: .init(threadId: threadID), body: .json(request)) {
        case .ok(let ok): return try ok.body.json
        case .notFound(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .conflict(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }
}
