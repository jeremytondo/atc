// Reading images off the pasteboard and out of drops for the composer: file
// URLs that point at images (Finder, the desktop) read as their file bytes
// with the filename; anything else that is image data (a screenshot, a copy
// from a browser) reads as PNG when the source offers it, else TIFF — the
// encoder converts what the server does not accept.

import AppKit
import Foundation
import UniformTypeIdentifiers

nonisolated enum ImagePasteboard {
    struct Image: Sendable {
        let data: Data
        let name: String?
    }

    /// Every image on `pasteboard`, file URLs first.
    static func images(from pasteboard: NSPasteboard) -> [Image] {
        let urls =
            (pasteboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true])
                as? [URL]) ?? []
        let files = urls.compactMap(image(at:))
        if !files.isEmpty { return files }
        for type in [NSPasteboard.PasteboardType.png, .tiff] {
            if let data = pasteboard.data(forType: type) { return [Image(data: data, name: nil)] }
        }
        return []
    }

    /// Every image among dropped item providers (file URLs or image data).
    static func images(from providers: [NSItemProvider]) async -> [Image] {
        var images: [Image] = []
        for provider in providers {
            if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier),
                let url = await loadURL(provider)
            {
                if let image = image(at: url) { images.append(image) }
                continue
            }
            guard let type = provider.registeredContentTypes.first(where: { $0.conforms(to: .image) }),
                let data = await loadData(provider, type: type)
            else { continue }
            images.append(Image(data: data, name: provider.suggestedName))
        }
        return images
    }

    /// The file's bytes when it is an image file the user can read.
    static func image(at url: URL) -> Image? {
        guard let type = UTType(filenameExtension: url.pathExtension), type.conforms(to: .image) else {
            return nil
        }
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        guard let data = try? Data(contentsOf: url) else { return nil }
        return Image(data: data, name: url.lastPathComponent)
    }

    private static func loadURL(_ provider: NSItemProvider) async -> URL? {
        await withCheckedContinuation { continuation in
            _ = provider.loadObject(ofClass: URL.self) { url, _ in
                continuation.resume(returning: url)
            }
        }
    }

    private static func loadData(_ provider: NSItemProvider, type: UTType) async -> Data? {
        await withCheckedContinuation { continuation in
            _ = provider.loadDataRepresentation(for: type) { data, _ in
                continuation.resume(returning: data)
            }
        }
    }
}
