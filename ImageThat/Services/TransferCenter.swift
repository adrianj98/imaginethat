import Foundation

/// How one file of a copy batch is getting on.
public enum TransferState: Equatable, Sendable {
    case queued, copying, done, cancelled
    case failed(String)
}

public struct TransferFile: Identifiable, Sendable {
    public let id = UUID()
    public let name: String
    public var done: Int64 = 0
    public var total: Int64 = 0
    /// Reads the engine has had to retry so far.
    public var retries: Int64 = 0
    public var state: TransferState = .queued
    public var startTime: Date?

    public var isFailed: Bool {
        if case .failed = state { return true }
        return false
    }
}

/// Files copied together: one import, or one item dragged to another vault.
public struct TransferBatch: Identifiable, Sendable {
    public let id = UUID()
    public let destination: String
    public let startTime = Date()
    public var files: [TransferFile]
    public var isRunning = true

    public var copiedCount: Int { files.filter { $0.state == .done }.count }
    public var failedCount: Int { files.filter(\.isFailed).count }
    public var cancelledCount: Int { files.filter { $0.state == .cancelled }.count }
    public var currentFile: TransferFile? { files.first { $0.state == .copying } }
    public var currentPosition: Int { (files.firstIndex { $0.state == .copying } ?? 0) + 1 }

    /// Finished files plus the part of the current one that has arrived.
    public var fraction: Double {
        guard !files.isEmpty else { return 0 }
        let finished = files.filter { $0.state != .queued && $0.state != .copying }.count
        var part = 0.0
        if let file = currentFile, file.total > 0 { part = Double(file.done) / Double(file.total) }
        return (Double(finished) + part) / Double(files.count)
    }

    public var title: String {
        let what = files.count == 1 ? files[0].name : "\(files.count) files"
        return "\(what) to \(destination)"
    }

    public var summary: String {
        var parts = ["\(copiedCount) of \(files.count) copied"]
        if failedCount > 0 { parts.append("\(failedCount) failed") }
        if !isRunning, cancelledCount > 0 { parts.append("\(cancelledCount) cancelled") }
        if isRunning, let file = currentFile, file.retries > 0 { parts.append("retrying") }
        return parts.joined(separator: " · ")
    }
}

/// Hands one file's progress to the TransferCenter, at most five times a
/// second however fast the bytes arrive.
public final class TransferReporter: @unchecked Sendable {
    private let send: @Sendable (Int64, Int64, Int64) -> Void
    private let lock = NSLock()
    private var lastSent = Date.distantPast
    private var counted: Int64 = 0

    init(send: @escaping @Sendable (Int64, Int64, Int64) -> Void) {
        self.send = send
    }

    public func report(done: Int64, total: Int64, retries: Int64 = 0) {
        lock.lock()
        let now = Date()
        let due = now.timeIntervalSince(lastSent) >= 0.2
        if due { lastSent = now }
        lock.unlock()
        if due { send(done, total, retries) }
    }

    /// Passes chunks through, reporting the bytes handed to the destination.
    public func counting(_ chunks: AsyncThrowingStream<Data, Error>,
                         total: Int64) -> AsyncMapSequence<AsyncThrowingStream<Data, Error>, Data> {
        chunks.map { [self] chunk in
            lock.lock()
            counted += Int64(chunk.count)
            let done = counted
            lock.unlock()
            report(done: done, total: total)
            return chunk
        }
    }
}

/// Runs every copy batch and remembers how each one went, for the Copies
/// window. Batches run side by side; the files of one batch run in order, and
/// a file that fails does not stop the rest. File names are decrypted, so this
/// stays in memory and is gone when the app quits.
@MainActor
public final class TransferCenter: ObservableObject {
    public struct Job: Sendable {
        public let name: String
        public let run: @Sendable (TransferReporter) async throws -> Void

        public init(name: String, run: @escaping @Sendable (TransferReporter) async throws -> Void) {
            self.name = name
            self.run = run
        }
    }

    /// Newest first.
    @Published public private(set) var batches: [TransferBatch] = []
    private var tasks: [UUID: Task<Void, Never>] = [:]

    public init() {}

    @discardableResult
    public func start(destination: String, jobs: [Job],
                      onFinish: @escaping @MainActor (TransferBatch) -> Void = { _ in }) -> UUID {
        let batch = TransferBatch(destination: destination, files: jobs.map { TransferFile(name: $0.name) })
        let id = batch.id
        batches.insert(batch, at: 0)
        tasks[id] = Task {
            await run(id, jobs)
            tasks[id] = nil
            update(id) { batch in
                batch.isRunning = false
                for index in batch.files.indices where [.queued, .copying].contains(batch.files[index].state) {
                    batch.files[index].state = .cancelled
                }
            }
            if let finished = batches.first(where: { $0.id == id }) { onFinish(finished) }
        }
        return id
    }

    private func run(_ id: UUID, _ jobs: [Job]) async {
        for (index, job) in jobs.enumerated() {
            guard !Task.isCancelled else { return }
            update(id) {
                $0.files[index].state = .copying
                $0.files[index].startTime = Date()
            }
            let reporter = TransferReporter { [weak self] done, total, retries in
                Task { @MainActor in self?.progress(id, index, done, total, retries) }
            }
            do {
                try await job.run(reporter)
                update(id) {
                    $0.files[index].state = .done
                    $0.files[index].done = $0.files[index].total
                }
            } catch {
                guard !Task.isCancelled, !(error is CancellationError) else { return }
                update(id) { $0.files[index].state = .failed(error.localizedDescription) }
            }
        }
    }

    /// Progress arrives on its own tasks, so some can land after the file ended.
    private func progress(_ id: UUID, _ index: Int, _ done: Int64, _ total: Int64, _ retries: Int64) {
        update(id) {
            guard $0.files[index].state == .copying else { return }
            $0.files[index].done = done
            $0.files[index].total = total
            $0.files[index].retries = retries
        }
    }

    private func update(_ id: UUID, _ change: (inout TransferBatch) -> Void) {
        guard let index = batches.firstIndex(where: { $0.id == id }) else { return }
        change(&batches[index])
    }

    public func cancel(_ id: UUID) {
        tasks[id]?.cancel()
    }

    public func clearFinished() {
        batches.removeAll { !$0.isRunning }
    }
}
