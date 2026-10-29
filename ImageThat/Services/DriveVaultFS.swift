import Foundation
import Darwin
import CLibGocryptfs

// File-scope C-compatible callbacks passed to GocryptfsOpenVFS.
// State is threaded through the opaque `ctx` pointer (an unretained DriveVaultFS).
// These closures capture nothing — they are valid @convention(c) function pointers.

let _driveVaultReadAtFn: VFSReadAtFn = { ctx, path, offset, length, buf, bufSize in
    guard let ctx, let path, let buf else { return -1 }
    let fs = Unmanaged<DriveVaultFS>.fromOpaque(ctx).takeUnretainedValue()
    return fs.syncReadAt(cPath: path, offset: offset, length: length, buf: buf, bufSize: bufSize)
}

let _driveVaultFileSizeFn: VFSFileSizeFn = { ctx, path in
    guard let ctx, let path else { return -1 }
    let fs = Unmanaged<DriveVaultFS>.fromOpaque(ctx).takeUnretainedValue()
    return fs.syncFileSize(cPath: path)
}

let _driveVaultListDirFn: VFSListDirFn = { ctx, path in
    guard let ctx, let path else { return ListResult(entries: nil, count: 0, error: nil) }
    let fs = Unmanaged<DriveVaultFS>.fromOpaque(ctx).takeUnretainedValue()
    return fs.syncListDir(cPath: path)
}

// MARK: - DriveVaultFS

/// Provides VFS callbacks for a gocryptfs session backed by Google Drive.
/// The path index maps vault-relative cipher paths to Drive file/folder IDs.
/// All sync* methods are called from Go goroutine threads and block via DispatchSemaphore.
final class DriveVaultFS: @unchecked Sendable {
    let rootFolderId: String
    let service: GoogleDriveService
    let cache: DriveCache

    private let lock = NSLock()
    // cipher dir path → Drive folder ID  ("" = vault root → rootFolderId)
    private var folderIds: [String: String]
    // cipher file path → Drive file info
    private var fileEntries: [String: FileEntry]
    // cipher dir paths that have been listed (prevents duplicate Drive API calls)
    private var listedDirs: Set<String>

    struct FileEntry {
        let id: String
        let modifiedTime: String
        let size: Int64
    }

    init(rootFolderId: String, service: GoogleDriveService, cache: DriveCache) {
        self.rootFolderId = rootFolderId
        self.service = service
        self.cache = cache
        self.folderIds = ["": rootFolderId]
        self.fileEntries = [:]
        self.listedDirs = []
    }

    // MARK: - Eagerly download gocryptfs.conf

    /// Downloads the raw gocryptfs.conf bytes from the root Drive folder.
    /// Must be called before passing this object to GocryptfsOpenVFS.
    func downloadConfig() async throws -> Data {
        let entries = try await service.listAllEntries(in: rootFolderId)

        // Populate root-level index entries now to avoid redundant fetches later.
        lock.lock()
        for entry in entries {
            if entry.isFolder {
                folderIds[entry.name] = entry.id
            } else {
                fileEntries[entry.name] = FileEntry(id: entry.id, modifiedTime: entry.modifiedTime, size: entry.size)
            }
        }
        listedDirs.insert("")
        lock.unlock()

        guard let conf = entries.first(where: { $0.name == "gocryptfs.conf" && !$0.isFolder }) else {
            throw GocryptfsError.configNotFound
        }
        return try await service.downloadData(fileId: conf.id)
    }

    // MARK: - Sync VFS callbacks (called from Go goroutine threads)

    // Files under this threshold are fully downloaded and cached on first access.
    // Larger files (videos) use range-reads with prefetched windows.
    private static let cacheThreshold: Int64 = 50 * 1_048_576   // 50 MB
    // On a range-cache miss we fetch a much larger window than gocryptfs asked for
    // in a single Drive HTTP request, so subsequent ciphertext-block reads (which
    // gocryptfs issues one ~4 KB block at a time) hit the cache instead of the
    // network. Without this, a 1 GB video would generate ~250k Drive round-trips.
    private static let prefetchWindow: Int64 = 8 * 1_048_576    // 8 MB

    func syncReadAt(cPath: UnsafePointer<CChar>, offset: Int64, length: Int64,
                    buf: UnsafeMutablePointer<UInt8>, bufSize: Int64) -> Int64 {
        let path = String(cString: cPath)
        ensureDirListed(parentOf: path)

        guard let entry = getFileEntry(path: path) else { return -1 }
        let toRead = Int(min(length, bufSize))
        guard toRead > 0 else { return 0 }

        // Small files: download the whole file once and serve from the actor cache.
        // The whole-file `DriveCache` is only consulted on this path — for large
        // files we skip it entirely so we don't pay an actor-hop + semaphore wait
        // on every single 4 KB ciphertext-block read (which happens hundreds of
        // thousands of times for a multi-GB video).
        if entry.size > 0 && entry.size <= Self.cacheThreshold {
            if let cached = cacheGet(fileId: entry.id, modifiedTime: entry.modifiedTime) {
                return copyFromData(cached, offset: offset, toRead: toRead, into: buf)
            }
            if let data = downloadAndCache(fileId: entry.id, modifiedTime: entry.modifiedTime) {
                return copyFromData(data, offset: offset, toRead: toRead, into: buf)
            }
            return -1
        }

        // Large files: check the synchronous range cache first.
        if let cached = DriveRangeCache.shared.read(fileId: entry.id, modifiedTime: entry.modifiedTime, offset: offset, length: toRead) {
            return copyFromData(cached, offset: 0, toRead: toRead, into: buf)
        }

        // Range-cache miss: fetch a prefetch window aligned to `prefetchWindow`,
        // populate the range cache from it, and serve the request out of memory.
        let totalSize = entry.size
        let alignedOffset = (offset / Self.prefetchWindow) * Self.prefetchWindow
        let windowEnd = min(alignedOffset + Self.prefetchWindow, totalSize)
        // The aligned window may not cover the full request (e.g. a read that
        // straddles a window boundary). Extend just enough to satisfy the caller.
        let requestEnd = min(offset + Int64(toRead), totalSize)
        let fetchEnd = max(windowEnd, requestEnd)
        let fetchLength = Int(fetchEnd - alignedOffset)
        guard fetchLength > 0 else { return 0 }

        let sema = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var result: Data? = nil
        let svc = service
        let fileId = entry.id
        let modTime = entry.modifiedTime
        Task.detached {
            result = try? await svc.downloadRange(fileId: fileId, offset: alignedOffset, length: fetchLength)
            sema.signal()
        }
        sema.wait()

        guard let data = result, !data.isEmpty else { return -1 }

        // Synchronous cache write so the next ciphertext-block read hits the cache.
        DriveRangeCache.shared.write(fileId: fileId, modifiedTime: modTime,
                                     totalSize: totalSize, offset: alignedOffset, data: data)

        let bytesIntoWindow = Int(offset - alignedOffset)
        let availableInWindow = data.count - bytesIntoWindow
        guard availableInWindow > 0 else { return 0 }
        let bytesToCopy = min(toRead, availableInWindow)
        data.withUnsafeBytes { ptr in
            let src = ptr.baseAddress!.advanced(by: bytesIntoWindow)
            buf.update(from: src.bindMemory(to: UInt8.self, capacity: bytesToCopy), count: bytesToCopy)
        }
        return Int64(bytesToCopy)
    }

    func syncFileSize(cPath: UnsafePointer<CChar>) -> Int64 {
        let path = String(cString: cPath)
        ensureDirListed(parentOf: path)
        return getFileEntry(path: path)?.size ?? -1
    }

    func syncListDir(cPath: UnsafePointer<CChar>) -> ListResult {
        let path = String(cString: cPath)

        // Ensure parent is listed so we have the folder ID for `path`.
        if !path.isEmpty {
            ensureDirListed(parentOf: path)
        }
        ensureDirListed(path)

        // Collect entries for this dir from the index.
        lock.lock()
        let prefix = path.isEmpty ? "" : "\(path)/"
        var entries: [(name: String, isDir: Bool, size: Int64)] = []
        for (k, v) in fileEntries where k.hasPrefix(prefix) {
            let remainder = String(k.dropFirst(prefix.count))
            guard !remainder.isEmpty, !remainder.contains("/") else { continue }
            entries.append((name: remainder, isDir: false, size: v.size))
        }
        for (k, _) in folderIds where k.hasPrefix(prefix) && k != path {
            let remainder = String(k.dropFirst(prefix.count))
            guard !remainder.isEmpty, !remainder.contains("/") else { continue }
            entries.append((name: remainder, isDir: true, size: 0))
        }
        lock.unlock()

        return buildListResult(entries)
    }

    // MARK: - Public accessors for DriveVaultBridge

    func getFileId(forCipherPath cipherPath: String) -> String? {
        lock.lock()
        defer { lock.unlock() }
        return fileEntries[cipherPath]?.id
    }

    func getFileEntry(path: String) -> FileEntry? {
        lock.lock()
        defer { lock.unlock() }
        return fileEntries[path]
    }

    func getFolderId(forCipherDir cipherDir: String) -> String? {
        lock.lock()
        defer { lock.unlock() }
        return folderIds[cipherDir]
    }

    /// Forget what is known about one cipher directory's children so the next
    /// listing fetches it again from Drive. Call after the app itself creates,
    /// uploads or deletes something in that directory; without it the index
    /// keeps serving the listing it fetched first, and new entries stay
    /// invisible until the vault is reopened.
    func invalidateListing(cipherDir: String) {
        lock.lock()
        defer { lock.unlock() }
        listedDirs.remove(cipherDir)
        let prefix = cipherDir.isEmpty ? "" : "\(cipherDir)/"
        func isDirectChild(_ key: String) -> Bool {
            guard key.hasPrefix(prefix), key != cipherDir else { return false }
            let rest = key.dropFirst(prefix.count)
            return !rest.isEmpty && !rest.contains("/")
        }
        for key in fileEntries.keys where isDirectChild(key) { fileEntries.removeValue(forKey: key) }
        for key in folderIds.keys where isDirectChild(key) {
            // Keep the folder ID of a directory that is still listed below us:
            // only drop it if nothing under it has been indexed.
            if !listedDirs.contains(key) { folderIds.removeValue(forKey: key) }
        }
    }

    // MARK: - Private

    private func parentOf(_ path: String) -> String {
        guard !path.isEmpty else { return "" }
        let p = (path as NSString).deletingLastPathComponent
        return p == "." ? "" : p
    }

    private func ensureDirListed(_ cipherPath: String) {
        lock.lock()
        guard !listedDirs.contains(cipherPath), let folderId = folderIds[cipherPath] else {
            lock.unlock()
            return
        }
        listedDirs.insert(cipherPath) // mark early to prevent concurrent redundant fetches
        lock.unlock()

        let sema = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var fetched: [DriveAllEntry] = []
        let svc = service
        Task.detached {
            fetched = (try? await svc.listAllEntries(in: folderId)) ?? []
            sema.signal()
        }
        sema.wait()

        lock.lock()
        for entry in fetched {
            let childPath = cipherPath.isEmpty ? entry.name : "\(cipherPath)/\(entry.name)"
            if entry.isFolder {
                folderIds[childPath] = entry.id
            } else {
                fileEntries[childPath] = FileEntry(id: entry.id, modifiedTime: entry.modifiedTime, size: entry.size)
            }
        }
        lock.unlock()
    }

    private func ensureDirListed(parentOf path: String) {
        ensureDirListed(parentOf(path))
    }

    private func downloadAndCache(fileId: String, modifiedTime: String) -> Data? {
        let sema = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var result: Data? = nil
        let svc = service
        let c = cache
        Task.detached {
            if let data = try? await svc.downloadData(fileId: fileId) {
                await c.store(fileId: fileId, modifiedTime: modifiedTime, data: data)
                result = data
            }
            sema.signal()
        }
        sema.wait()
        return result
    }

    private func copyFromData(_ data: Data, offset: Int64, toRead: Int, into buf: UnsafeMutablePointer<UInt8>) -> Int64 {
        let available = data.count - Int(offset)
        guard available > 0 else { return 0 }
        let bytesToCopy = min(toRead, available)
        data.withUnsafeBytes { ptr in
            let src = ptr.baseAddress!.advanced(by: Int(offset))
            buf.update(from: src.bindMemory(to: UInt8.self, capacity: bytesToCopy), count: bytesToCopy)
        }
        return Int64(bytesToCopy)
    }

    private func cacheGet(fileId: String, modifiedTime: String) -> Data? {
        let sema = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var result: Data? = nil
        let c = cache
        Task.detached {
            result = await c.get(fileId: fileId, modifiedTime: modifiedTime)
            sema.signal()
        }
        sema.wait()
        return result
    }

    private func buildListResult(_ entries: [(name: String, isDir: Bool, size: Int64)]) -> ListResult {
        guard !entries.isEmpty else {
            return ListResult(entries: nil, count: 0, error: nil)
        }
        let count = entries.count
        guard let rawPtr = malloc(MemoryLayout<DirEntry>.stride * count) else {
            return ListResult(entries: nil, count: 0, error: strdup("malloc failed"))
        }
        let cEntries = rawPtr.bindMemory(to: DirEntry.self, capacity: count)
        for (i, e) in entries.enumerated() {
            cEntries[i] = DirEntry(name: strdup(e.name), is_dir: e.isDir ? 1 : 0, size: e.size)
        }
        return ListResult(entries: cEntries, count: Int32(count), error: nil)
    }
}
