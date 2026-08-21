// Attachment thumbnails on user rows and in the composer (ATC-216), and the
// full-size viewer a thumbnail opens. A thumbnail shows either local bytes
// (a pending echo, the composer) or a server attachment read through the
// row actions' loader; the viewer adds the filename and "Copy path" — the
// stable server path any agent on that machine can be pointed at.

import ATCAppServerAPI
import ATCDesign
import CoreGraphics
import SwiftUI

/// What a thumbnail renders: bytes already here, or an attachment to read.
public enum AttachmentImage: Identifiable {
    case local(PendingAttachment)
    case remote(Components.Schemas.ThreadAttachment)

    public var id: String {
        switch self {
        case .local(let pending): pending.id.uuidString
        case .remote(let attachment): attachment.id
        }
    }

    var name: String {
        switch self {
        case .local(let pending): pending.name
        case .remote(let attachment): attachment.name
        }
    }

    /// The server path, once the attachment has one.
    var path: String? {
        if case .remote(let attachment) = self { return attachment.path }
        return nil
    }

    /// The decoded image: local bytes decode here, a server attachment goes
    /// through `remote` (the row actions' loader).
    func load(remote: (Components.Schemas.ThreadAttachment) async -> CGImage?) async -> CGImage? {
        switch self {
        case .local(let pending): ImageAttachmentEncoder.decode(pending.data)
        case .remote(let attachment): await remote(attachment)
        }
    }
}

/// A row of thumbnails, each opening the viewer. `remove` adds an ✕ badge
/// (the composer); rows pass nil.
public struct AttachmentStrip: View {
    let images: [AttachmentImage]
    var remove: ((AttachmentImage) -> Void)?

    @State private var viewing: AttachmentImage?

    public init(images: [AttachmentImage], remove: ((AttachmentImage) -> Void)? = nil) {
        self.images = images
        self.remove = remove
    }

    public var body: some View {
        HStack(spacing: Spacing.sm) {
            ForEach(images) { image in
                Button {
                    viewing = image
                } label: {
                    AttachmentThumbnail(image: image)
                }
                .buttonStyle(.plain)
                .help(image.name)
                .accessibilityLabel("Image \(image.name)")
                .overlay(alignment: .topTrailing) {
                    if let remove {
                        Button {
                            remove(image)
                        } label: {
                            Image(systemName: "xmark.circle.fill")
                                .symbolRenderingMode(.palette)
                                .foregroundStyle(.white, .black.opacity(0.6))
                        }
                        .buttonStyle(.plain)
                        .offset(x: 4, y: -4)
                        .help("Remove \(image.name)")
                        .accessibilityLabel("Remove \(image.name)")
                    }
                }
            }
        }
        .sheet(item: $viewing) { image in
            AttachmentViewer(image: image)
        }
    }
}

/// One thumbnail: a fixed square that shows the image once decoded.
struct AttachmentThumbnail: View {
    let image: AttachmentImage
    var side: CGFloat = 72

    @Environment(\.chatRowActions) private var actions
    @State private var decoded: CGImage?

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: Radius.chip).fill(Surface.raised)
            if let decoded {
                Image(decorative: decoded, scale: 1)
                    .resizable()
                    .scaledToFill()
            } else {
                Image(systemName: "photo")
                    .foregroundStyle(.tertiary)
            }
        }
        .frame(width: side, height: side)
        .clipShape(RoundedRectangle(cornerRadius: Radius.chip))
        .task(id: image.id) { decoded = await image.load(remote: actions.loadAttachment) }
    }
}

/// The full-size view of one attachment.
private struct AttachmentViewer: View {
    let image: AttachmentImage

    @Environment(\.dismiss) private var dismiss
    @Environment(\.chatRowActions) private var actions
    @State private var decoded: CGImage?

    var body: some View {
        VStack(spacing: Spacing.md) {
            HStack {
                Text(image.name).font(.headline).lineLimit(1)
                Spacer()
                if let path = image.path {
                    Button("Copy path") { Clipboard.copy(path) }
                        .help(path)
                }
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
            Group {
                if let decoded {
                    Image(decorative: decoded, scale: 1)
                        .resizable()
                        .scaledToFit()
                } else {
                    ProgressView()
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .padding(Spacing.lg)
        .frame(minWidth: 480, minHeight: 360)
        .task { decoded = await image.load(remote: actions.loadAttachment) }
    }
}
