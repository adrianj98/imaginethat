import SwiftUI

struct EditingToolbarView: View {
    @ObservedObject var pipeline: ImageEditingPipeline
    var onReset: () -> Void
    var onSaveReplace: () async -> Void
    var onSaveCopy: () async -> Void
    var onCancel: () -> Void
    var isSourceWritable: Bool

    @State private var isSaving = false

    var body: some View {
        VStack(spacing: 0) {
            Divider()

            ScrollView(.horizontal, showsIndicators: false) {
                VStack(spacing: 12) {
                    // Row 1: Brightness, Contrast, Saturation
                    HStack(spacing: 16) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Brightness").font(.caption2).foregroundStyle(.secondary)
                            Slider(value: Binding(
                                get: { pipeline.brightness },
                                set: { pipeline.brightness = $0 }
                            ), in: -1...1)
                                .frame(width: 120)
                        }

                        VStack(alignment: .leading, spacing: 4) {
                            Text("Contrast").font(.caption2).foregroundStyle(.secondary)
                            Slider(value: Binding(
                                get: { pipeline.contrast },
                                set: { pipeline.contrast = $0 }
                            ), in: 0...2)
                                .frame(width: 120)
                        }

                        VStack(alignment: .leading, spacing: 4) {
                            Text("Saturation").font(.caption2).foregroundStyle(.secondary)
                            Slider(value: Binding(
                                get: { pipeline.saturation },
                                set: { pipeline.saturation = $0 }
                            ), in: 0...2)
                                .frame(width: 120)
                        }

                        Divider().frame(height: 40)
                    }

                    // Row 2: Sharpness, Rotation, Crop
                    HStack(spacing: 16) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Sharpness").font(.caption2).foregroundStyle(.secondary)
                            Slider(value: Binding(
                                get: { pipeline.sharpness },
                                set: { pipeline.sharpness = $0 }
                            ), in: 0...1)
                                .frame(width: 120)
                        }

                        HStack(spacing: 8) {
                            Button(action: rotateLeft) {
                                Image(systemName: "rotate.left")
                                    .frame(width: 24, height: 24)
                            }
                            .buttonStyle(.plain)
                            .help("Rotate left")

                            Button(action: rotateRight) {
                                Image(systemName: "rotate.right")
                                    .frame(width: 24, height: 24)
                            }
                            .buttonStyle(.plain)
                            .help("Rotate right")
                        }

                        Button(action: { pipeline.reset() }) {
                            Text("Reset")
                        }
                        .buttonStyle(.bordered)

                        Divider().frame(height: 40)

                        // Save actions
                        HStack(spacing: 8) {
                            if isSourceWritable {
                                Button(action: saveReplace) {
                                    Text("Replace")
                                }
                                .buttonStyle(.bordered)
                                .help("Save over original file")
                            }

                            Button(action: saveCopy) {
                                Text("Save Copy")
                            }
                            .buttonStyle(.bordered)
                            .help("Save as new file")

                            Button(action: onCancel) {
                                Text("Cancel")
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
            }
        }
        .frame(height: 100)
        .background(Color(nsColor: .controlBackgroundColor))
        .disabled(isSaving)
    }

    private func rotateLeft() {
        pipeline.rotation = (pipeline.rotation - 90 + 360) % 360
    }

    private func rotateRight() {
        pipeline.rotation = (pipeline.rotation + 90) % 360
    }

    private func saveReplace() {
        isSaving = true
        Task {
            await onSaveReplace()
            isSaving = false
        }
    }

    private func saveCopy() {
        isSaving = true
        Task {
            await onSaveCopy()
            isSaving = false
        }
    }
}
