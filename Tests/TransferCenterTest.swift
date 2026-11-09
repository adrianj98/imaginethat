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

        center.clearFinished()
        check("Clear Finished empties the list", center.batches.isEmpty, "\(center.batches.count) left")
    }

    private final class Flag: @unchecked Sendable {
        private let lock = NSLock()
        private var value = false
        var isSet: Bool { lock.lock(); defer { lock.unlock() }; return value }
        func set() { lock.lock(); value = true; lock.unlock() }
    }
}
