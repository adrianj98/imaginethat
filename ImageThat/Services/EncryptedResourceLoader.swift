import AVFoundation
import UniformTypeIdentifiers
import os

private let logger = Logger(subsystem: "com.imagethat", category: "EncryptedResourceLoader")

/// The reads EncryptedResourceLoader needs from a vault. GocryptfsBridge is the
/// app's implementation; tests supply one that simulates a slow drive.
public protocol EncryptedRangeReading: AnyObject, Sendable {
    func readRange(path: String, offset: Int64, length: Int32) throws -> Data
    func fileSize(path: String) throws -> Int64
}

extension GocryptfsBridge: EncryptedRangeReading {}

/// AVAssetResourceLoaderDelegate that streams decrypted gocryptfs content to AVPlayer.
/// Decrypted data stays in memory — never written to disk.
public final class EncryptedResourceLoader: NSObject, AVAssetResourceLoaderDelegate, @unchecked Sendable {
    private let bridge: any EncryptedRangeReading
    private let relativePath: String
    private var fileSize: Int64?
    private let contentType: String
    // Set when this loader is released with its player item. AVFoundation
    // drops in-flight requests at that point without cancelling them, so
    // `isCancelled` alone would leave a loop reading to EOF over the network.
    private let released = ReleaseFlag()

    public init(bridge: any EncryptedRangeReading, relativePath: String) {
        self.bridge = bridge
        self.relativePath = relativePath

        // Determine content type as UTI (required by AVAssetResourceLoadingContentInformationRequest)
        let ext = (relativePath as NSString).pathExtension.lowercased()
        self.contentType = UTType(filenameExtension: ext)?.identifier ?? UTType.data.identifier

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

        // Handle data request asynchronously — reading can take a long time for large files
        // and blocking the resource loader queue prevents AVFoundation from issuing
        // cancellations or follow-up byte-range requests.
        if let dataRequest = loadingRequest.dataRequest {
            let offset = dataRequest.requestedOffset
            let length = dataRequest.requestedLength
            let toEOF = dataRequest.requestsAllDataToEndOfResource

            logger.info("Data request: offset=\(offset) length=\(length) toEOF=\(toEOF)")

            let bridge = self.bridge
            let relativePath = self.relativePath
            let chunkSize: Int32 = 256 * 1024 // 256KB chunks
            let released = self.released

            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    var bytesRead: Int64 = 0
                    var currentOffset = offset
                    let totalRequested: Int64 = toEOF ? Int64.max : Int64(length)

                    while bytesRead < totalRequested {
                        // Stop reading if AVFoundation cancelled this request or the
                        // player that wanted it is gone
                        if loadingRequest.isCancelled || released.isSet { return }

                        let remaining = totalRequested - bytesRead
                        let toRead = Int32(min(Int64(chunkSize), remaining))

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

                        if data.count < Int(toRead) {
                            logger.info("Short read, EOF")
                            break
                        }
                    }

                    logger.info("Fulfilled \(bytesRead) bytes (toEOF=\(toEOF))")
                    loadingRequest.finishLoading()
                } catch {
                    logger.error("readRange error: \(error.localizedDescription, privacy: .public)")
                    loadingRequest.finishLoading(with: error)
                }
            }
            return true
        }

        loadingRequest.finishLoading()
        return true
    }

    deinit {
        released.set()
    }

    public func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        didCancel loadingRequest: AVAssetResourceLoadingRequest
    ) {
        // Nothing to clean up — no background tasks
    }
}

/// A one-way flag shared between the loader and its background read loops.
private final class ReleaseFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false

    var isSet: Bool {
        lock.lock(); defer { lock.unlock() }
        return value
    }

    func set() {
        lock.lock(); value = true; lock.unlock()
    }
}
