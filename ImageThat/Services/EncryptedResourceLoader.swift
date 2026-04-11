import AVFoundation
import os

private let logger = Logger(subsystem: "com.imagethat", category: "EncryptedResourceLoader")

/// AVAssetResourceLoaderDelegate that streams decrypted gocryptfs content to AVPlayer.
/// Decrypted data stays in memory — never written to disk.
public final class EncryptedResourceLoader: NSObject, AVAssetResourceLoaderDelegate, @unchecked Sendable {
    private let bridge: GocryptfsBridge
    private let relativePath: String
    private var fileSize: Int64?
    private let contentType: String

    public init(bridge: GocryptfsBridge, relativePath: String) {
        self.bridge = bridge
        self.relativePath = relativePath

        // Determine content type from extension
        let ext = (relativePath as NSString).pathExtension.lowercased()
        switch ext {
        case "mp4", "m4v":
            self.contentType = "video/mp4"
        case "mov":
            self.contentType = "video/quicktime"
        case "avi":
            self.contentType = "video/avi"
        case "mkv":
            self.contentType = "video/x-matroska"
        default:
            self.contentType = "application/octet-stream"
        }

        super.init()
    }

    public func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        shouldWaitForLoadingOfRequestedResource loadingRequest: AVAssetResourceLoadingRequest
    ) -> Bool {
        // Handle content information request
        if let contentRequest = loadingRequest.contentInformationRequest {
            do {
                let size = try bridge.fileSize(path: relativePath)
                self.fileSize = size
                contentRequest.contentLength = size
                contentRequest.contentType = contentType
                contentRequest.isByteRangeAccessSupported = true
                logger.info("Content info: path=\(self.relativePath, privacy: .public) size=\(size) type=\(self.contentType)")
            } catch {
                logger.error("Content info error: \(error.localizedDescription, privacy: .public)")
                loadingRequest.finishLoading(with: error)
                return true
            }
        }

        // Handle data request
        if let dataRequest = loadingRequest.dataRequest {
            let offset = dataRequest.requestedOffset
            let length = dataRequest.requestedLength

            logger.info("Data request: offset=\(offset) length=\(length) (will chunk into \(256 * 1024) byte chunks)")

            // For large requests, just read what we can quickly and let AVPlayer request more
            let chunkSize: Int32 = 256 * 1024 // 256KB chunks

            do {
                var bytesRead: Int64 = 0
                var currentOffset = offset

                // Respond with up to 10MB at a time, then finish and let AVPlayer request more
                let maxBufferPerRequest: Int64 = 10 * 1024 * 1024 // 10MB
                let totalRequested = Int64(length)
                while bytesRead < maxBufferPerRequest && bytesRead < totalRequested {
                    let toRead = Int32(min(Int64(chunkSize), totalRequested - bytesRead))
                    logger.info("readRange offset=\(currentOffset) length=\(toRead)")

                    let data = try bridge.readRange(
                        path: relativePath,
                        offset: currentOffset,
                        length: toRead
                    )

                    if data.isEmpty {
                        logger.info("EOF at offset=\(currentOffset)")
                        break
                    }

                    dataRequest.respond(with: data)
                    bytesRead += Int64(data.count)
                    currentOffset += Int64(data.count)

                    if data.count < toRead {
                        logger.info("Short read, EOF")
                        break
                    }
                }

                logger.info("Responding with \(bytesRead)/\(totalRequested) bytes")
                loadingRequest.finishLoading()
            } catch {
                logger.error("readRange error: \(error.localizedDescription, privacy: .public)")
                loadingRequest.finishLoading(with: error)
            }
            return true
        }

        loadingRequest.finishLoading()
        return true
    }

    public func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        didCancel loadingRequest: AVAssetResourceLoadingRequest
    ) {
        // Nothing to clean up — no background tasks
    }
}
