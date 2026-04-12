import SwiftUI

struct CropOverlayView: View {
    @ObservedObject var pipeline: ImageEditingPipeline
    var imageSize: CGSize
    var onCropDone: () -> Void

    @State private var selectionStart: CGPoint = .zero
    @State private var selectionEnd: CGPoint = .zero
    @State private var isDragging = false

    var selectionRect: CGRect {
        guard isDragging || (selectionStart != .zero && selectionEnd != .zero) else { return .zero }
        let minX = min(selectionStart.x, selectionEnd.x)
        let minY = min(selectionStart.y, selectionEnd.y)
        let maxX = max(selectionStart.x, selectionEnd.x)
        let maxY = max(selectionStart.y, selectionEnd.y)
        return CGRect(x: minX, y: minY, width: maxX - minX, height: maxY - minY)
    }

    var body: some View {
        ZStack {
            // Semi-transparent overlay outside the selection
            Rectangle()
                .fill(Color.black.opacity(0.4))
                .frame(width: imageSize.width, height: imageSize.height)

            // Selection border
            if !selectionRect.isEmpty {
                ZStack {
                    Rectangle()
                        .stroke(Color.blue, lineWidth: 2)

                    // Corner and edge handles for future drag-to-resize
                    HStack(spacing: selectionRect.width - 16) {
                        VStack(spacing: selectionRect.height - 16) {
                            Circle().fill(Color.blue).frame(width: 8, height: 8)
                            Spacer()
                            Circle().fill(Color.blue).frame(width: 8, height: 8)
                        }
                        Spacer()
                        VStack(spacing: selectionRect.height - 16) {
                            Circle().fill(Color.blue).frame(width: 8, height: 8)
                            Spacer()
                            Circle().fill(Color.blue).frame(width: 8, height: 8)
                        }
                    }
                    .frame(width: selectionRect.width, height: selectionRect.height)
                }
                .frame(width: selectionRect.width, height: selectionRect.height)
                .position(x: selectionRect.midX, y: selectionRect.midY)
            }

            // Instructions
            VStack {
                Text(isDragging ? "Release to crop" : "Drag to select crop area")
                    .font(.caption)
                    .foregroundStyle(.white)
                    .padding(8)
                    .background(Color.black.opacity(0.6))
                    .cornerRadius(4)
                Spacer()
            }
            .padding(16)
        }
        .frame(width: imageSize.width, height: imageSize.height)
        .gesture(
            DragGesture(minimumDistance: 10)
                .onChanged { value in
                    isDragging = true
                    selectionStart = value.startLocation
                    selectionEnd = value.location
                }
                .onEnded { value in
                    isDragging = false
                    selectionEnd = value.location
                    applyCrop()
                    onCropDone()
                }
        )
    }

    private func applyCrop() {
        guard !selectionRect.isEmpty else { return }
        // Store crop rect in image coordinates
        pipeline.cropRect = selectionRect
    }
}
