import SwiftUI

struct ImageInfoPanelView: View {
    var tags: [String]
    var text: String
    var isAnalyzing: Bool
    var onClose: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Image Analysis")
                    .font(.headline)
                Spacer()
                Button(action: onClose) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .help("Close")
            }
            .padding(.bottom, 8)

            // Tags section
            VStack(alignment: .leading, spacing: 8) {
                Text("Auto-Tags").font(.subheadline).foregroundStyle(.secondary)
                if isAnalyzing && tags.isEmpty {
                    HStack(spacing: 8) {
                        ProgressView()
                            .scaleEffect(0.8, anchor: .center)
                        Text("Analyzing...")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                } else if tags.isEmpty {
                    Text("No tags found").font(.caption).foregroundStyle(.tertiary)
                } else {
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(Array(tags.enumerated()), id: \.offset) { index, tag in
                            if index % 2 == 0 {
                                HStack(spacing: 6) {
                                    Text(tags[index])
                                        .font(.caption)
                                        .padding(.horizontal, 8)
                                        .padding(.vertical, 4)
                                        .background(Color.blue.opacity(0.2))
                                        .cornerRadius(4)

                                    if index + 1 < tags.count {
                                        Text(tags[index + 1])
                                            .font(.caption)
                                            .padding(.horizontal, 8)
                                            .padding(.vertical, 4)
                                            .background(Color.blue.opacity(0.2))
                                            .cornerRadius(4)
                                    }
                                    Spacer()
                                }
                            }
                        }
                    }
                }
            }

            Divider()

            // Text section
            VStack(alignment: .leading, spacing: 8) {
                Text("Recognized Text").font(.subheadline).foregroundStyle(.secondary)
                if isAnalyzing && text.isEmpty {
                    HStack(spacing: 8) {
                        ProgressView()
                            .scaleEffect(0.8, anchor: .center)
                        Text("Analyzing...")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                } else if text.isEmpty {
                    Text("No text found").font(.caption).foregroundStyle(.tertiary)
                } else {
                    ScrollView {
                        Text(text)
                            .font(.caption)
                            .lineLimit(nil)
                            .padding(8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 150)
                    .background(Color(nsColor: .controlBackgroundColor))
                    .cornerRadius(4)
                }
            }

            Spacer()
        }
        .padding(16)
        .frame(maxWidth: 280)
        .background(Color(nsColor: .windowBackgroundColor))
    }
}
