import CoreGraphics
import Foundation
import ImageIO
import Testing
import UniformTypeIdentifiers

@testable import ATCChat

/// The encoder's fitting rules: supported formats under the cap pass
/// through untouched, unsupported formats convert (alpha → PNG, opaque →
/// JPEG), oversized images shrink until they fit, and non-images are refused.
@Suite("ImageAttachmentEncoder")
struct ImageAttachmentEncoderTests {
    /// A solid (or translucent) image of the given size, encoded as `type`.
    private func image(width: Int, height: Int, alpha: Bool, as type: UTType) throws -> Data {
        let space = try #require(CGColorSpace(name: CGColorSpace.sRGB))
        // Deterministic per-pixel noise (a linear congruential sequence)
        // defeats every encoder's compression, so a large image really is
        // large at full size — the same bytes every run.
        var pixels = [UInt8](repeating: 0, count: width * height * 4)
        var seed: UInt32 = 7
        for index in stride(from: 0, to: pixels.count, by: 4) {
            seed = seed &* 1_664_525 &+ 1_013_904_223
            // Premultiplied: color never exceeds the alpha byte.
            let mask: UInt32 = alpha ? 0x7f : 0xff
            pixels[index] = UInt8(truncatingIfNeeded: (seed >> 24) & mask)
            pixels[index + 1] = UInt8(truncatingIfNeeded: (seed >> 16) & mask)
            pixels[index + 2] = UInt8(truncatingIfNeeded: (seed >> 8) & mask)
            pixels[index + 3] = alpha ? 0x80 : 0xff
        }
        let cgImage = try #require(
            pixels.withUnsafeMutableBytes { buffer in
                CGContext(
                    data: buffer.baseAddress, width: width, height: height, bitsPerComponent: 8,
                    bytesPerRow: width * 4, space: space,
                    bitmapInfo: (alpha ? CGImageAlphaInfo.premultipliedLast : .noneSkipLast).rawValue
                )?.makeImage()
            })
        let data = NSMutableData()
        let destination = try #require(
            CGImageDestinationCreateWithData(data, type.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(destination, cgImage, nil)
        #expect(CGImageDestinationFinalize(destination))
        return data as Data
    }

    @Test("a small PNG passes through byte for byte, keeping the user's stem")
    func passThrough() throws {
        let png = try image(width: 8, height: 8, alpha: true, as: .png)
        let prepared = try ImageAttachmentEncoder.prepare(png, name: "Screenshot 1.png")
        #expect(prepared.data == png)
        #expect(prepared.mediaType == .imagePng)
        #expect(prepared.name == "Screenshot 1.png")
    }

    @Test("TIFF converts: alpha becomes PNG, opaque becomes JPEG, and the extension follows")
    func convertsUnsupported() throws {
        let translucent = try image(width: 8, height: 8, alpha: true, as: .tiff)
        let png = try ImageAttachmentEncoder.prepare(translucent, name: "shot.tiff")
        #expect(png.mediaType == .imagePng)
        #expect(png.name == "shot.png")
        #expect(ImageAttachmentEncoder.decode(png.data) != nil)

        let opaque = try image(width: 8, height: 8, alpha: false, as: .tiff)
        let jpeg = try ImageAttachmentEncoder.prepare(opaque, name: nil)
        #expect(jpeg.mediaType == .imageJpeg)
        #expect(jpeg.name == "image.jpeg")
    }

    @Test("an oversized image shrinks until it fits the cap")
    func shrinksToFit() throws {
        // A lowered cap keeps the fixture small; the rule is the same.
        let cap = 64 * 1024
        let big = try image(width: 480, height: 480, alpha: false, as: .png)
        try #require(big.count > cap)
        let prepared = try ImageAttachmentEncoder.prepare(big, name: "big.png", maxBytes: cap)
        #expect(prepared.data.count <= cap)
        #expect(prepared.mediaType == .imageJpeg)
        #expect(prepared.name == "big.jpeg")
        let decoded = try #require(ImageAttachmentEncoder.decode(prepared.data))
        #expect(decoded.width < 480)
    }

    @Test("an image that cannot fit the cap is refused")
    func refusesUnfittable() throws {
        let png = try image(width: 64, height: 64, alpha: true, as: .png)
        #expect(throws: ImageAttachmentError.cannotFit) {
            try ImageAttachmentEncoder.prepare(png, name: "tiny.png", maxBytes: 16)
        }
    }

    @Test("non-image bytes are refused")
    func refusesNonImages() {
        #expect(throws: ImageAttachmentError.notAnImage) {
            try ImageAttachmentEncoder.prepare(Data("hello".utf8), name: "notes.txt")
        }
    }
}
