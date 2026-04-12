import SwiftUI
import AVKit
import Vision
import CoreGraphics

public struct DetailView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    @State private var currentImage: NSImage?
    @State private var sourceCGImage: CGImage?
    @State private var isLoadingMedia = false
    @GestureState private var dragOffset: CGFloat = 0
    @State private var zoomScale: CGFloat = 1.0
    @GestureState private var magnifyDelta: CGFloat = 1.0

    // Editing state
    @State private var isEditing = false
    @State private var pipeline = ImageEditingPipeline()
    @State private var editedImage: NSImage?
    @State private var isCropping = false

    // Vision analysis state
    @State private var showInfoPanel = false
    @State private var visionTags: [String] = []
    @State private var visionText: String = ""
    @State private var isAnalyzing = false
    @State private var visionService = VisionAnalysisService()

    public init() {}

    public var body: some View {
        ZStack(alignment: .topTrailing) {
            GeometryReader { geo in
                ZStack {
                    Color(nsColor: .windowBackgroundColor).ignoresSafeArea()

                    if let item = appState.selectedItem {
                        ZStack {
                            if item.mediaType == .video {
                                VideoPlayerView(item: item)
                            } else {
                                imageView
                            }

                            // Crop overlay (if in crop mode)
                            if isCropping, let cgImage = sourceCGImage {
                                CropOverlayView(
                                    pipeline: pipeline,
                                    imageSize: CGSize(width: cgImage.width, height: cgImage.height),
                                    onCropDone: { isCropping = false }
                                )
                            }
                        }
                        .scaleEffect(zoomScale * magnifyDelta)
                        .gesture(
                            MagnificationGesture()
                                .updating($magnifyDelta) { value, state, _ in state = value }
                                .onEnded { value in
                                    zoomScale = max(1.0, min(zoomScale * value, 10.0))
                                }
                        )
                        .contentShape(Rectangle())
                        .onScrollWheel { delta in
                            zoomScale = max(1.0, min(zoomScale * (1 - delta.scrollingDeltaY * 0.01), 10.0))
                        }
                    }

                    // Navigation arrows
                    HStack {
                        navButton(systemName: "chevron.left", enabled: appState.selectedIndex > 0) {
                            appState.navigatePrevious()
                        }
                        Spacer()
                        navButton(systemName: "chevron.right", enabled: appState.selectedIndex < appState.mediaItems.count - 1) {
                            appState.navigateNext()
                        }
                    }
                    .padding(.horizontal, 8)
                }
                .gesture(zoomScale > 1.0 ? nil : swipeGesture)
            }

            // Top-right toolbar buttons
            if appState.selectedItem?.mediaType == .image {
                HStack(spacing: 8) {
                    Button(action: { showInfoPanel.toggle() }) {
                        Image(systemName: "info.circle")
                    }
                    .buttonStyle(.plain)
                    .help("Show image info")

                    if !isEditing {
                        Button(action: { isEditing = true }) {
                            Image(systemName: "slider.horizontal.3")
                        }
                        .buttonStyle(.plain)
                        .help("Edit image")
                    }
                }
                .padding(12)
            }

            // Info panel (if shown)
            if showInfoPanel {
                ImageInfoPanelView(
                    tags: visionTags,
                    text: visionText,
                    isAnalyzing: isAnalyzing,
                    onClose: { showInfoPanel = false }
                )
                .frame(maxHeight: .infinity, alignment: .topTrailing)
                .transition(.move(edge: .trailing))
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if isEditing {
                EditingToolbarView(
                    pipeline: pipeline,
                    onReset: { pipeline.reset() },
                    onSaveReplace: { await saveImageReplace() },
                    onSaveCopy: { await saveImageCopy() },
                    onCancel: { cancelEditing() },
                    isSourceWritable: appState.isSourceWritable
                )
            }
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(phases: .down) { keyPress in
            if shortcuts.matches(keyPress, action: .previousImage) {
                appState.navigatePrevious()
                return .handled
            }
            if shortcuts.matches(keyPress, action: .nextImage) {
                appState.navigateNext()
                return .handled
            }
            if shortcuts.matches(keyPress, action: .backToGrid) {
                appState.exitDetail()
                return .handled
            }
            return .ignored
        }
        .onChange(of: appState.selectedIndex) { _, _ in
            // Reset editing but keep info panel open and re-analyze
            isEditing = false
            isCropping = false
            pipeline.reset()
            editedImage = nil
            visionTags = []
            visionText = ""
            zoomScale = 1.0
            loadCurrentImage()
            // If info panel is open, analyze the new image
            if showInfoPanel {
                analyzeImageWithVision()
            }
        }
        .onReceive(pipeline.objectWillChange) { _ in
            updateEditedImage()
        }
        .onChange(of: showInfoPanel) { _, newValue in
            if newValue {
                analyzeImageWithVision()
            }
        }
        .onAppear {
            loadCurrentImage()
        }
    }

    @ViewBuilder
    private var imageView: some View {
        if isLoadingMedia {
            ProgressView()
                .progressViewStyle(.circular)
                .scaleEffect(1.5)
        } else if let image = editedImage ?? currentImage {
            Image(nsImage: image)
                .resizable()
                .aspectRatio(contentMode: .fit)
                .offset(x: dragOffset)
        }
    }

    private var swipeGesture: some Gesture {
        DragGesture(minimumDistance: 50)
            .updating($dragOffset) { value, state, _ in
                state = value.translation.width
            }
            .onEnded { value in
                let threshold: CGFloat = 100
                if value.translation.width > threshold {
                    appState.navigatePrevious()
                } else if value.translation.width < -threshold {
                    appState.navigateNext()
                }
            }
    }

    private func navButton(systemName: String, enabled: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemName)
                .font(.title)
                .foregroundStyle(.primary)
                .padding(12)
                .background(.ultraThinMaterial)
                .clipShape(Circle())
        }
        .buttonStyle(.plain)
        .opacity(enabled ? 1 : 0.3)
        .disabled(!enabled)
    }

    private func loadCurrentImage() {
        guard let item = appState.selectedItem, item.mediaType == .image else {
            currentImage = nil
            sourceCGImage = nil
            return
        }
        isLoadingMedia = true
        Task {
            guard let source = appState.mediaSource else { return }
            do {
                let image = try await source.loadImage(for: item)
                self.currentImage = image
                // Also capture the CGImage for editing
                if let cgImage = image.cgImage(forProposedRect: nil, context: nil, hints: nil) {
                    self.sourceCGImage = cgImage
                } else if let tiffData = image.tiffRepresentation,
                          let bitmap = NSBitmapImageRep(data: tiffData),
                          let cgImage = bitmap.cgImage {
                    self.sourceCGImage = cgImage
                }
            } catch {
                self.currentImage = nil
                self.sourceCGImage = nil
            }
            isLoadingMedia = false
        }
    }

    private func updateEditedImage() {
        guard let sourceCG = sourceCGImage else { return }
        editedImage = pipeline.render(source: sourceCG)
    }

    private func resetEditingState() {
        isEditing = false
        isCropping = false
        pipeline.reset()
        editedImage = nil
        visionTags = []
        visionText = ""
        isAnalyzing = false
    }

    private func cancelEditing() {
        resetEditingState()
    }

    private func saveImageReplace() async {
        guard let item = appState.selectedItem, let source = appState.mediaSource as? WritableMediaSource else { return }
        guard let sourceCG = sourceCGImage else { return }

        let format = item.url.pathExtension.lowercased() == "png" ? "png" : "jpeg"
        let imageData = pipeline.exportData(source: sourceCG, format: format)

        do {
            try await source.saveImage(imageData, for: item)
            appState.invalidateThumbnail(for: item)
            resetEditingState()
        } catch {
            appState.errorMessage = "Failed to save image: \(error.localizedDescription)"
        }
    }

    private func saveImageCopy() async {
        guard let item = appState.selectedItem, let source = appState.mediaSource as? WritableMediaSource else { return }
        guard let sourceCG = sourceCGImage else { return }

        let format = item.url.pathExtension.lowercased() == "png" ? "png" : "jpeg"
        let imageData = pipeline.exportData(source: sourceCG, format: format)

        do {
            let newItem = try await source.saveImageCopy(imageData, for: item, suffix: "-edited")
            appState.insertMediaItem(newItem, after: item)
            resetEditingState()
        } catch {
            appState.errorMessage = "Failed to save image copy: \(error.localizedDescription)"
        }
    }

    private func analyzeImageWithVision() {
        guard let cgImage = sourceCGImage else { return }
        isAnalyzing = true
        visionTags = []
        visionText = ""

        Task {
            do {
                async let tags = visionService.classifyImage(cgImage)
                async let text = visionService.recognizeText(in: cgImage)
                self.visionTags = try await tags
                self.visionText = try await text
            } catch {
                // Silently fail — Vision isn't critical
            }
            isAnalyzing = false
        }
    }
}

extension View {
    func onScrollWheel(_ handler: @escaping (NSEvent) -> Void) -> some View {
        return self.background(ScrollWheelHandler(handler: handler))
    }
}

struct ScrollWheelHandler: NSViewRepresentable {
    let handler: (NSEvent) -> Void

    func makeNSView(context: Context) -> NSView {
        let view = ScrollWheelView(handler: handler)
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {}
}

class ScrollWheelView: NSView {
    let handler: (NSEvent) -> Void

    init(handler: @escaping (NSEvent) -> Void) {
        self.handler = handler
        super.init(frame: .zero)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    override func scrollWheel(with event: NSEvent) {
        handler(event)
        super.scrollWheel(with: event)
    }
}
