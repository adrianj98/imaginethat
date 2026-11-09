import SwiftUI

/// The Copies window: every copy batch since the app started.
public struct CopiesView: View {
    public static let windowID = "copies"

    @ObservedObject private var transfers: TransferCenter

    public init(transfers: TransferCenter) {
        self.transfers = transfers
    }

    public var body: some View {
        Group {
            if transfers.batches.isEmpty {
                Text("Nothing has been copied since ImageThat opened.")
                    .font(.system(size: 13))
                    .foregroundStyle(Glass.label)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVStack(spacing: 12) {
                        ForEach(transfers.batches) { batch in
                            BatchGroup(batch: batch) { transfers.cancel(batch.id) }
                        }
                    }
                    .padding(20)
                }
            }
        }
        .frame(minWidth: 460, idealWidth: 560, minHeight: 300, idealHeight: 480)
        .background(Glass.lightCanvas)
        .toolbar {
            Button("Clear Finished") { transfers.clearFinished() }
                .disabled(!transfers.batches.contains { !$0.isRunning })
        }
    }
}

/// One batch: what it is, how far it has got, and its files on request.
private struct BatchGroup: View {
    let batch: TransferBatch
    let cancel: () -> Void

    /// nil until the chevron is used: open only when something failed.
    @State private var chosenExpanded: Bool?

    private var expanded: Bool { chosenExpanded ?? (batch.failedCount > 0) }

    var body: some View {
        SettingsGroup {
            SettingsRow(title: batch.title, subtitle: batch.summary) {
                if batch.isRunning {
                    Button("Cancel", action: cancel)
                        .buttonStyle(PillButtonStyle(height: 28))
                        .font(.system(size: 12))
                }
                Button {
                    chosenExpanded = !expanded
                } label: {
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                        .font(.system(size: 11, weight: .semibold))
                        .frame(width: 24, height: 24)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help(expanded ? "Hide Files" : "Show Files")
            }
            if batch.isRunning {
                ProgressView(value: batch.fraction)
                    .padding(.horizontal, 14)
                    .padding(.bottom, 10)
            }
            if expanded {
                ForEach(batch.files) { file in
                    Divider().padding(.leading, 14)
                    FileRow(file: file)
                }
            }
        }
    }
}

private struct FileRow: View {
    let file: TransferFile

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            icon.font(.system(size: 12))
            VStack(alignment: .leading, spacing: 1) {
                Text(file.name)
                    .font(.system(size: 12))
                    .lineLimit(1)
                    .truncationMode(.middle)
                if case .failed(let message) = file.state {
                    Text(message)
                        .font(.system(size: 11))
                        .foregroundStyle(Color(red: 0xB3 / 255, green: 0x26 / 255, blue: 0x1E / 255))
                        .textSelection(.enabled)
                }
            }
            Spacer(minLength: 8)
            Text(detail)
                .font(.system(size: 11))
                .foregroundStyle(Glass.label)
                .monospacedDigit()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 6)
    }

    @ViewBuilder private var icon: some View {
        switch file.state {
        case .queued: Image(systemName: "clock").foregroundStyle(Glass.label)
        case .copying: Image(systemName: "arrow.right.circle").foregroundStyle(Color.accentColor)
        case .done: Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .cancelled: Image(systemName: "minus.circle").foregroundStyle(Glass.label)
        case .failed: Image(systemName: "exclamationmark.circle.fill").foregroundStyle(.red)
        }
    }

    private var detail: String {
        let size = ByteCountFormatter.string(fromByteCount: file.total, countStyle: .file)
        switch file.state {
        case .queued: return "Waiting"
        case .copying:
            let done = ByteCountFormatter.string(fromByteCount: file.done, countStyle: .file)
            return file.retries > 0 ? "\(done) of \(size), \(file.retries) retries" : "\(done) of \(size)"
        case .done: return size
        case .cancelled: return "Cancelled"
        case .failed: return file.retries > 0 ? "Gave up after \(file.retries) retries" : "Failed"
        }
    }
}

/// The card over the main window while something is copying.
struct CopyProgressCard: View {
    @ObservedObject var transfers: TransferCenter
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        let running = transfers.batches.filter(\.isRunning)
        if let batch = running.first, let file = batch.currentFile {
            let elapsed = Date().timeIntervalSince(file.startTime ?? Date())
            let bytesPerSecond = elapsed > 0 ? Double(file.done) / elapsed : 0

            VStack(spacing: 12) {
                HStack {
                    Text(status(batch, file))
                        .font(.subheadline)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer()
                    Button(running.count > 1 ? "Show All (\(running.count))" : "Show All") {
                        openWindow(id: CopiesView.windowID)
                    }
                    .buttonStyle(.link)
                    .font(.caption)
                    Button(action: { transfers.cancel(batch.id) }) {
                        Image(systemName: "xmark.circle.fill")
                            .font(.system(size: 14))
                    }
                    .buttonStyle(.plain)
                }
                ProgressView(value: batch.fraction)
                HStack {
                    Text("\(file.done / 1_000_000) / \(file.total / 1_000_000) MB")
                        .font(.caption)
                    Spacer()
                    Text(String(format: "%.1f MB/s", bytesPerSecond / 1_000_000))
                        .font(.caption)
                        .fontWeight(.semibold)
                        .foregroundStyle(.blue)
                    Spacer()
                    Text(String(format: "%.0fs", elapsed))
                        .font(.caption)
                }
                .foregroundStyle(.secondary)
            }
            .padding()
            .frame(maxWidth: 520)
            .glassCard()
            .padding(20)
        }
    }

    private func status(_ batch: TransferBatch, _ file: TransferFile) -> String {
        let position = batch.files.count > 1 ? " (\(batch.currentPosition) of \(batch.files.count))" : ""
        return "Copying \(file.name)\(position)..."
    }
}
