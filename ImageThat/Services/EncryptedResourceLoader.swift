import AVFoundation

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
            } catch {
                loadingRequest.finishLoading(with: error)
                return true
            }
        }

        // Handle data request
        if let dataRequest = loadingRequest.dataRequest {
            let offset = dataRequest.requestedOffset
            let length = dataRequest.requestedLength

            // Read in chunks to avoid huge allocations
            let chunkSize: Int32 = 256 * 1024 // 256KB chunks
            var currentOffset = offset
            var remaining = Int32(length)

            do {
                while remaining > 0 {
                    let toRead = min(chunkSize, remaining)
                    let data = try bridge.readRange(
                        path: relativePath,
                        offset: currentOffset,
                        length: toRead
                    )

                    if data.isEmpty {
                        break
                    }

                    dataRequest.respond(with: data)
                    currentOffset += Int64(data.count)
                    remaining -= Int32(data.count)

                    if data.count < Int(toRead) {
                        break // EOF
                    }
                }
                loadingRequest.finishLoading()
            } catch {
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
