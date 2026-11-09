import Foundation
import ImageThatCore

/// The Copies window's model: batches run side by side, a failed file doesn't
/// stop its batch, and Cancel stops only what it was pressed on.
enum TransferCenterTest {
    private struct Boom: LocalizedError {
        var errorDescription: String? { "disk on fire" }
    }

    /// Lets a test hold a job open until it says go.
    private actor Gate {
        private var open = false
        private var waiters: [CheckedContinuation<Void, Never>] = []

        func wait() async {
            if open { return }
            await withCheckedContinuation { waiters.append($0) }
        }

        func release() {
            open = true
            waiters.forEach { $0.resume() }
            waiters = []
        }
    }

    @MainActor
    private static func finish(_ center: TransferCenter, _ destination: String,
                               _ jobs: [TransferCenter.Job]) async -> TransferBatch {
        await withCheckedContinuation { cont in
            center.start(destination: destination, jobs: jobs) { cont.resume(returning: $0) }
        }
    }

    @MainActor
    static func run(_ check: (String, Bool, String) -> Void) async {
        print("\n--- Copy Batches ---")
        let ok: @Sendable (TransferReporter) async throws -> Void = { _ in }

        // A failure is recorded and the rest of the batch still runs.
        let center = TransferCenter()
        let mixed = await finish(center, "Photos", [
            .init(name: "a.jpg", run: ok),
            .init(name: "b.jpg") { _ in throw Boom() },
            .init(name: "c.jpg", run: ok),
        ])
        check("a failed file doesn't stop its batch", mixed.copiedCount == 2 && mixed.failedCount == 1, mixed.summary)
        check("the failure says why", mixed.files[1].state == .failed("disk on fire"), "\(mixed.files[1].state)")
        check("a finished batch isn't running", !mixed.isRunning, "")
        check("the batch names its files and destination", mixed.title == "3 files to Photos", mixed.title)

        // A new batch leaves the running one alone; Cancel stops only its own.
        let gate = Gate()
        let cancelled = Flag()
        let slow = TransferCenter.Job(name: "slow.mov") { _ in
            await gate.wait()
        }
        let cancellable = TransferCenter.Job(name: "stuck.mov") { _ in
            await withTaskCancellationHandler {
                await gate.wait()
            } onCancel: {
                cancelled.set()
                Task { await gate.release() }
            }
            try Task.checkCancellation()
        }
        let never = Flag()
        let notReached = TransferCenter.Job(name: "after.mov") { _ in never.set() }

        let other = TransferCenter()
        async let first = finish(other, "Videos", [slow])
        let second = other.start(destination: "Clips", jobs: [cancellable, notReached])
        for _ in 0..<200 where other.batches.first(where: { $0.id == second })?.currentFile == nil {
            try? await Task.sleep(for: .milliseconds(5))
        }
        other.cancel(second)
        let firstDone = await first
        check("a second batch doesn't cancel the first", firstDone.copiedCount == 1, firstDone.summary)
        for _ in 0..<200 where other.batches.contains(where: \.isRunning) {
            try? await Task.sleep(for: .milliseconds(10))
        }
        let stopped = other.batches.first { $0.id == second }
        check("Cancel reaches the running file", cancelled.isSet, "")
        check("Cancel stops the files after it", !never.isSet, "")
        check("cancelled files aren't failures",
              stopped?.failedCount == 0 && stopped?.cancelledCount == 2, stopped?.summary ?? "no batch")

        // Real bytes: progress comes from what reaches the destination.
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("transfer-\(UUID().uuidString)")
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: dir) }
            let payload = Data(repeating: 7, count: 3_000_000)
            let source = dir.appendingPathComponent("in.bin")
            try payload.write(to: source)
            let dest = FolderSource(rootURL: dir)
            let total = Int64(payload.count)
            let batch = await finish(center, "Temp", [.init(name: "in.bin") { reporter in
                reporter.report(done: 0, total: total)
                let chunks = FolderSource(rootURL: dir).readChunks(for: MediaItem(url: source), chunkSize: 1_048_576)
                try await dest.writeChunks(reporter.counting(chunks, total: total), to: "out.bin", totalSize: total)
            }])
            let written = try Data(contentsOf: dir.appendingPathComponent("out.bin"))
            check("a job copies the bytes", written == payload, "\(written.count) bytes")
            check("a finished file shows its whole size", batch.files[0].done == total && batch.files[0].total == total,
                  "\(batch.files[0].done)/\(batch.files[0].total)")
        } catch {
            check("copy batch over real files", false, error.localizedDescription)
        }

        // Retry Failed runs only the failed files, in place.
        let runs = Counter(), flakyRuns = Counter()
        let retryCenter = TransferCenter()
        let finishes = Counter()
        let retriedID = retryCenter.start(destination: "Photos", jobs: [
            .init(name: "fine.jpg") { _ in runs.bump() },
            .init(name: "flaky.jpg") { _ in
                if flakyRuns.bump() == 1 { throw Boom() }
            },
        ]) { _ in finishes.bump() }
        for _ in 0..<200 where retryCenter.batches[0].isRunning {
            try? await Task.sleep(for: .milliseconds(5))
        }
        check("a batch can fail before its retry", retryCenter.batches[0].failedCount == 1, retryCenter.batches[0].summary)
        retryCenter.retryFailed(retriedID)
        check("a retried batch runs again", retryCenter.batches[0].isRunning, "")
        for _ in 0..<200 where retryCenter.batches[0].isRunning {
            try? await Task.sleep(for: .milliseconds(5))
        }
        check("Retry Failed fixes the failed file", retryCenter.batches[0].copiedCount == 2, retryCenter.batches[0].summary)
        check("Retry Failed leaves copied files alone", runs.count == 1, "\(runs.count) runs")
        check("a retry stays in the same batch", retryCenter.batches.count == 1, "\(retryCenter.batches.count)")
        check("a retry reports its finish too", finishes.count == 2, "\(finishes.count)")

        // A file already there is neither copied again nor a failure.
        let skipped = await finish(retryCenter, "Photos", [
            .init(name: "there.jpg") { _ in throw TransferSkip(size: 42) },
        ])
        check("a skipped file isn't a failure", skipped.failedCount == 0 && skipped.skippedCount == 1, skipped.summary)
        check("a skipped file says so", skipped.summary.contains("1 already there"), skipped.summary)

        await runWholeFileRetries(check)
        await runFolderWrites(check)

        center.clearFinished()
        check("Clear Finished empties the list", center.batches.isEmpty, "\(center.batches.count) left")
    }

    private static func quickRetry(attempts: Int = 40, landed: Bool = false,
                                   _ send: @Sendable () async throws -> Void) async throws {
        try await TransferRetry.run(attempts: attempts, baseDelay: .milliseconds(1), maxDelay: .milliseconds(1),
                                    reporter: TransferReporter { _, _, _ in }, landed: { landed }, send)
    }

    private static func runWholeFileRetries(_ check: (String, Bool, String) -> Void) async {
        let blips = Counter()
        let survived: Bool
        do {
            try await quickRetry {
                if blips.bump() < 3 { throw URLError(.networkConnectionLost) }
            }
            survived = true
        } catch { survived = false }
        check("an upload is sent again after a blip", survived && blips.count == 3, "\(blips.count) tries")

        let refused = Counter()
        var stopped = false
        do {
            try await quickRetry {
                _ = refused.bump()
                throw RemoteServerError.http(status: 403, message: "no")
            }
        } catch { stopped = true }
        check("a refused upload isn't sent again", stopped && refused.count == 1, "\(refused.count) tries")

        let endless = Counter()
        var gaveUp = false
        do {
            try await quickRetry(attempts: 4) {
                _ = endless.bump()
                throw RemoteServerError.http(status: 503, message: "busy")
            }
        } catch { gaveUp = true }
        check("an upload gives up in the end", gaveUp && endless.count == 4, "\(endless.count) tries")

        let lost = Counter()
        var landedOK = false
        do {
            try await quickRetry(landed: true) {
                _ = lost.bump()
                throw URLError(.timedOut)
            }
            landedOK = true
        } catch {}
        check("an upload whose reply was lost counts as done", landedOK && lost.count == 1, "\(lost.count) tries")

        let shareError = NSError(domain: NSCocoaErrorDomain, code: 512, userInfo: [
            NSUnderlyingErrorKey: NSError(domain: NSPOSIXErrorDomain, code: Int(EIO)),
        ])
        check("a share's I/O error can clear up", TransferRetry.isTransient(shareError), "")
        check("cancelling isn't retried", !TransferRetry.isTransient(CancellationError()), "")
        check("a full disk isn't retried",
              !TransferRetry.isTransient(NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC))), "")
    }

    private static func runFolderWrites(_ check: (String, Bool, String) -> Void) async {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("folder-\(UUID().uuidString)")
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let folder = FolderSource(rootURL: dir)

        let failing = AsyncThrowingStream<Data, Error> { continuation in
            continuation.yield(Data(repeating: 1, count: 100))
            continuation.finish(throwing: Boom())
        }
        try? await folder.writeChunks(failing, to: "partial.bin", totalSize: 200)
        let afterFailure = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? ["?"]
        check("a failed folder copy leaves nothing", afterFailure.isEmpty, "\(afterFailure)")

        func stream(_ byte: UInt8, _ count: Int) -> AsyncThrowingStream<Data, Error> {
            AsyncThrowingStream { $0.yield(Data(repeating: byte, count: count)); $0.finish() }
        }
        try? await folder.writeChunks(stream(2, 50), to: "kept.bin", totalSize: 50)
        var refused = false
        do { try await folder.writeChunks(stream(3, 80), to: "kept.bin", totalSize: 80) } catch { refused = true }
        let kept = try? Data(contentsOf: dir.appendingPathComponent("kept.bin"))
        check("a folder copy won't replace a file", refused && kept == Data(repeating: 2, count: 50), "")
        let knownSize = await folder.existingFileSize(at: "kept.bin")
        let noSize = await folder.existingFileSize(at: "missing.bin")
        check("a folder reports what is already there", knownSize == 50 && noSize == nil, "\(String(describing: knownSize))")
        let left = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []
        check("a folder copy cleans up its temp files", left == ["kept.bin"], "\(left)")
    }

    private final class Counter: @unchecked Sendable {
        private let lock = NSLock()
        private var value = 0
        var count: Int { lock.lock(); defer { lock.unlock() }; return value }
        @discardableResult func bump() -> Int { lock.lock(); defer { lock.unlock() }; value += 1; return value }
    }

    private final class Flag: @unchecked Sendable {
        private let lock = NSLock()
        private var value = false
        var isSet: Bool { lock.lock(); defer { lock.unlock() }; return value }
        func set() { lock.lock(); value = true; lock.unlock() }
    }
}
