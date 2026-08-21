// Images the composer holds before a send (ATC-216): the bytes stay on the
// client until the prompt goes, and every image is made to fit the server's
// caps first — the accepted formats (PNG, JPEG, GIF, WebP) and the 8 MB
// per-image limit. ImageIO only, so iOS shares the code; nothing here talks
// to the server.
//
// Fitting rules: a supported format under the cap passes through byte for
// byte (no recompression of what the user pasted). Anything else — an
// unsupported format (TIFF from the pasteboard, HEIC from Photos) or an
// oversized image — is decoded and re-encoded as PNG when it has alpha,
// JPEG otherwise, at progressively smaller scales until it fits. A GIF that
// needs re-encoding loses its animation (it becomes a PNG of the first
// frame) — the cap wins. The filename keeps the user's stem and takes the
// extension of the format actually sent.

import ATCAppServerAPI
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

public typealias AttachmentMediaType = Components.Schemas.AttachmentMediaType

/// An image ready to upload with the next prompt.
public nonisolated struct PendingAttachment: Identifiable, Equatable, Sendable {
    public let id: UUID
    public let data: Data
    public let mediaType: AttachmentMediaType
    public let name: String

    public init(id: UUID = UUID(), data: Data, mediaType: AttachmentMediaType, name: String) {
        self.id = id
        self.data = data
        self.mediaType = mediaType
        self.name = name
    }
}

public nonisolated enum ImageAttachmentError: LocalizedError, Equatable {
    case notAnImage
    case cannotFit

    public var errorDescription: String? {
        switch self {
        case .notAnImage: "That is not an image."
        case .cannotFit: "The image cannot be made small enough to send."
        }
    }
}

// Nonisolated so the composer can run `prepare` off the main actor: a large
// image decodes and re-encodes for long enough to freeze typing otherwise.
public nonisolated enum ImageAttachmentEncoder {
    /// The server's per-image cap.
    public static let maxBytes = 8 * 1024 * 1024
    /// The server's per-prompt cap.
    public static let maxPerPrompt = 10

    /// Scales tried when an image must shrink, largest first.
    private static let scales: [CGFloat] = [1, 0.75, 0.5, 0.35, 0.25, 0.15, 0.1]

    /// Makes `data` an attachment the server accepts (see the header).
    /// `maxBytes` is the server's cap; tests lower it.
    public static func prepare(_ data: Data, name: String? = nil, maxBytes: Int = maxBytes) throws
        -> PendingAttachment
    {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            CGImageSourceGetCount(source) > 0
        else { throw ImageAttachmentError.notAnImage }
        let sourceType = (CGImageSourceGetType(source) as String?).flatMap(UTType.init)
        if let mediaType = sourceType.flatMap(AttachmentMediaType.init(type:)), data.count <= maxBytes {
            return PendingAttachment(data: data, mediaType: mediaType, name: fileName(name, for: mediaType))
        }
        guard let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
            throw ImageAttachmentError.notAnImage
        }
        let mediaType: AttachmentMediaType = hasAlpha(image) ? .imagePng : .imageJpeg
        for scale in scales {
            guard let scaled = scale == 1 ? image : resized(image, scale: scale),
                let encoded = encode(scaled, as: mediaType)
            else { continue }
            if encoded.count <= maxBytes {
                return PendingAttachment(data: encoded, mediaType: mediaType, name: fileName(name, for: mediaType))
            }
        }
        throw ImageAttachmentError.cannotFit
    }

    /// Decodes an image for display (thumbnails, the viewer).
    public static func decode(_ data: Data) -> CGImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(source, 0, nil)
    }

    private static func hasAlpha(_ image: CGImage) -> Bool {
        switch image.alphaInfo {
        case .none, .noneSkipFirst, .noneSkipLast: false
        default: true
        }
    }

    private static func resized(_ image: CGImage, scale: CGFloat) -> CGImage? {
        let width = max(1, Int(CGFloat(image.width) * scale))
        let height = max(1, Int(CGFloat(image.height) * scale))
        guard
            let context = CGContext(
                data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                space: CGColorSpace(name: CGColorSpace.sRGB)!,
                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        context.interpolationQuality = .high
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        return context.makeImage()
    }

    private static func encode(_ image: CGImage, as mediaType: AttachmentMediaType) -> Data? {
        let data = NSMutableData()
        guard
            let destination = CGImageDestinationCreateWithData(
                data, mediaType.utType.identifier as CFString, 1, nil)
        else { return nil }
        let options: [CFString: Any] =
            mediaType == .imageJpeg ? [kCGImageDestinationLossyCompressionQuality: 0.85] : [:]
        CGImageDestinationAddImage(destination, image, options as CFDictionary)
        guard CGImageDestinationFinalize(destination) else { return nil }
        return data as Data
    }

    /// The user's stem (or "image") with the sent format's extension.
    private static func fileName(_ name: String?, for mediaType: AttachmentMediaType) -> String {
        let trimmed = name?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let stem = trimmed.isEmpty ? "image" : (trimmed as NSString).deletingPathExtension
        return "\(stem.isEmpty ? "image" : stem).\(mediaType.utType.preferredFilenameExtension ?? "img")"
    }
}

nonisolated extension AttachmentMediaType {
    public var utType: UTType {
        switch self {
        case .imagePng: .png
        case .imageJpeg: .jpeg
        case .imageGif: .gif
        case .imageWebp: .webP
        }
    }

    init?(type: UTType) {
        guard let match = Self.allCases.first(where: { type.conforms(to: $0.utType) }) else { return nil }
        self = match
    }
}
