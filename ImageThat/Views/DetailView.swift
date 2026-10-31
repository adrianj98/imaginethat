import SwiftUI
import AVKit
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
    @State private var editMode: EditMode = .adjust
    @State private var cropAspect: CropAspect = .free
    @State private var isSaving = false

    @AppStorage("viewerShowInfo") private var showInfo = true

    // Auto-hide arrows on mouse idle.
    @State private var showArrows = true
    @State private var arrowHideTask: Task<Void, Never>?

    private var isCropping: Bool { isEditing && editMode == .crop }
    private var panelVisible: Bool { isEditing || showInfo }
    private var trailingInset: CGFloat { panelVisible ? 316 : 16 }

    public init() {}

    public var body: some View {
        ZStack(alignment: .top) {
            Glass.darkCanvas.ignoresSafeArea()

            stage
                .padding(.top, 76)
                .padding(.leading, 32)
                .padding(.trailing, panelVisible ? 316 : 32)
                .padding(.bottom, isEditing ? 44 : 16)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .gesture(zoomScale > 1.0 || isEditing ? nil : swipeGesture)

            if !isEditing {
                // Navigation arrows (auto-hide on mouse idle, but stay
                // visible while the mouse is hovering over either button)
                HStack {
                    navButton(systemName: "chevron.left", help: "Previous", enabled: appState.selectedIndex > 0) {
                        appState.navigatePrevious()
                    }
                    .onHover { hovering in handleArrowHover(hovering) }
                    Spacer()
                    navButton(systemName: "chevron.right", help: "Next", enabled: appState.selectedIndex < appState.mediaItems.count - 1) {
                        appState.navigateNext()
                    }
                    .onHover { hovering in handleArrowHover(hovering) }
                }
                .padding(.leading, 24)
                .padding(.trailing, panelVisible ? 316 : 24)
                .frame(maxHeight: .infinity)
                .opacity(showArrows ? 1 : 0)
                .animation(.easeOut(duration: 0.25), value: showArrows)
            }

            MouseTrackingView(
                onMouseMoved: {
                    showArrows = true
                    scheduleArrowHide()
                },
                onMouseExited: { scheduleArrowHide() }
            )
            .allowsHitTesting(false)

            Group {
                if isEditing { editHeader } else { viewHeader }
            }
            .padding(.top, 16)
            .padding(.leading, 16)
            .padding(.trailing, trailingInset)

            if let item = appState.selectedItem {
                Group {
                    if isEditing {
                        EditPanelView(pipeline: pipeline, mode: editMode, aspect: $cropAspect,
                                      fileName: item.name, isSourceWritable: appState.isSourceWritable)
                    } else if showInfo {
                        ItemInfoCard(item: item, dark: true,
                                     image: item.mediaType == .image ? sourceCGImage : nil,
                                     loadsImage: false,
                                     location: locationLabel,
                                     footnote: infoFootnote(for: item))
                    }
                }
                .frame(width: 284)
                .padding(16)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
                .transition(.move(edge: .trailing).combined(with: .opacity))
            }

            if isEditing {
                Text(editHint)
                    .font(.system(size: 12))
                    .foregroundStyle(Glass.label)
                    .padding(.leading, 32)
                    .padding(.trailing, 316)
                    .padding(.bottom, 20)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
            }
        }
        .environment(\.colorScheme, .dark)
        .disabled(isSaving)
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
            resetEditingState()
            zoomScale = 1.0
            loadCurrentImage()
        }
        .onReceive(pipeline.objectWillChange) { _ in
            // objectWillChange fires before the value lands; render after it.
            DispatchQueue.main.async { updateEditedImage() }
        }
        .onChange(of: editMode) { _, _ in updateEditedImage() }
        .onChange(of: cropAspect) { _, _ in updateEditedImage() }
        .onAppear {
            loadCurrentImage()
            scheduleArrowHide()
        }
        .onDisappear { arrowHideTask?.cancel() }
    }

    // MARK: - Stage

    @ViewBuilder
    private var stage: some View {
        if let item = appState.selectedItem {
            ZStack {
                if item.mediaType == .video {
                    VideoPlayerView(item: item)
                } else {
                    imageView
                }

                if isCropping, let shown = editedImage ?? currentImage {
                    CropOverlayView(pipeline: pipeline, imageSize: shown.size,
                                    aspect: cropAspect.ratio(for: shown.size))
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
                guard !isCropping else { return }
                zoomScale = max(1.0, min(zoomScale * (1 - delta.scrollingDeltaY * 0.01), 10.0))
            }
        }
    }

    // MARK: - Floating toolbars

    private var viewHeader: some View {
        HStack(spacing: 10) {
            SidebarToggle(dark: true)

            Button(action: { appState.exitDetail() }) {
                Label("Grid", systemImage: "square.grid.2x2")
                    .font(.system(size: 13))
                    .padding(.leading, 12)
                    .padding(.trailing, 16)
                    .frame(height: 40)
                    .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .glassCapsule(dark: true)
            .help("Back to grid")

            HStack(spacing: 6) {
                if let folder = folderName {
                    Text(folder).font(.system(size: 13)).foregroundStyle(.secondary).lineLimit(1)
                    Image(systemName: "chevron.right")
                        .font(.system(size: 9, weight: .bold))
                        .foregroundStyle(.tertiary)
                }
                Text(appState.selectedItem?.name ?? "")
                    .font(.system(size: 15, weight: .semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(appState.currentItemLabel)
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .padding(.leading, 6)
                    .fixedSize()
            }
            .padding(.horizontal, 16)
            .glassCapsule(dark: true)

            Spacer(minLength: 0)

            if appState.selectedItem?.mediaType == .image {
                HStack(spacing: 2) {
                    GlassIconButton(systemImage: "minus", help: "Zoom Out", dark: true) {
                        zoomScale = max(1.0, zoomScale / 1.25)
                    }
                    Text(zoomScale <= 1.0 ? "Fit" : "\(Int((zoomScale * 100).rounded()))%")
                        .font(.system(size: 12))
                        .monospacedDigit()
                        .frame(minWidth: 44)
                    GlassIconButton(systemImage: "plus", help: "Zoom In", dark: true) {
                        zoomScale = min(10.0, zoomScale * 1.25)
                    }
                }
                .padding(.horizontal, 4)
                .glassCapsule(dark: true)
            }

            HStack(spacing: 2) {
                GlassIconButton(systemImage: "pencil", help: "Edit — images only", dark: true) {
                    zoomScale = 1.0
                    editMode = .adjust
                    isEditing = true
                }
                .disabled(appState.selectedItem?.mediaType != .image)
                .opacity(appState.selectedItem?.mediaType == .image ? 1 : 0.4)
                GlassIconButton(systemImage: "sidebar.right", help: showInfo ? "Hide Info" : "Show Info", isOn: showInfo, dark: true) {
                    withAnimation(.easeOut(duration: 0.2)) { showInfo.toggle() }
                }
                .keyboardShortcut("i", modifiers: [.option, .command])
            }
            .padding(.horizontal, 4)
            .glassCapsule(dark: true)
        }
        .frame(height: 48)
    }

    private var editHeader: some View {
        HStack(spacing: 10) {
            HStack(spacing: 6) {
                Text(appState.selectedItem?.name ?? "")
                    .font(.system(size: 15, weight: .semibold))
                    .lineLimit(1)
                if pipeline.isModified {
                    Text("Edited").font(.system(size: 12)).foregroundStyle(Glass.edited).padding(.leading, 6)
                }
            }
            .padding(.horizontal, 16)
            .glassCapsule(dark: true)

            Spacer(minLength: 0)

            HStack(spacing: 2) {
                ForEach(EditMode.allCases, id: \.self) { mode in
                    Button(mode.rawValue) { editMode = mode }
                        .buttonStyle(.plain)
                        .font(.system(size: 13, weight: .medium))
                        .padding(.horizontal, 16)
                        .frame(height: 32)
                        .background(Capsule().fill(editMode == mode ? Color.white.opacity(0.16) : .clear))
                        .contentShape(Capsule())
                        .accessibilityAddTraits(editMode == mode ? .isSelected : [])
                }
            }
            .padding(.horizontal, 4)
            .glassCapsule(dark: true)

            Spacer(minLength: 0)

            HStack(spacing: 2) {
                Button("Cancel") { resetEditingState() }
                    .buttonStyle(PillButtonStyle(kind: .quiet, dark: true))
                    .keyboardShortcut(.cancelAction)
                Button("Save Copy") { save { await saveImageCopy() } }
                    .buttonStyle(PillButtonStyle(kind: appState.isSourceWritable ? .quiet : .prominent, dark: true))
                if appState.isSourceWritable {
                    Button("Replace") { save { await saveImageReplace() } }
                        .buttonStyle(PillButtonStyle(kind: .prominent, dark: true))
                }
            }
            .padding(.horizontal, 4)
            .glassCapsule(dark: true)
        }
        .frame(height: 48)
    }

    private var folderName: String? {
        guard let source = appState.mediaSource else { return nil }
        let last = source.currentPath.split(separator: "/").last.map(String.init)
        return last ?? source.rootDisplayName
    }

    private var locationLabel: String? {
        guard let source = appState.mediaSource else { return nil }
        let path = source.currentPath
        return path.isEmpty ? source.rootDisplayName : "\(source.rootDisplayName) / \(path)"
    }

    private func infoFootnote(for item: MediaItem) -> String? {
        let encrypted = appState.mediaSource is VaultSource || appState.mediaSource is DriveVaultSource
            || appState.mediaSource is RemoteVaultSource
        guard encrypted else { return nil }
        return item.mediaType == .video
            ? "Streamed out of the vault and decrypted in small ranges as it plays."
            : "Decrypted in memory only."
    }

    private var editHint: String {
        let size = (editedImage ?? currentImage).map { "\(Int($0.size.width)) × \(Int($0.size.height))" } ?? ""
        if isCropping {
            guard let crop = pipeline.cropRect, let shown = editedImage ?? currentImage else {
                return "Drag across the image to crop"
            }
            let w = Int(crop.width * shown.size.width), h = Int(crop.height * shown.size.height)
            return "Drag across the image to crop · \(w) × \(h) selected"
        }
        return size.isEmpty ? "Edits preview live" : "Edits preview live · \(size)"
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
                .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                .shadow(color: .black.opacity(0.6), radius: 30, y: 20)
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

    private func navButton(systemName: String, help: String, enabled: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemName)
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(.white)
                .frame(width: 44, height: 44)
                .glassCard(dark: true, radius: 22)
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(help)
        .opacity(enabled ? 1 : 0.3)
        .disabled(!enabled)
    }

    private func scheduleArrowHide() {
        arrowHideTask?.cancel()
        arrowHideTask = Task { @MainActor in
            try? await Task.sleep(for: .seconds(2))
            if !Task.isCancelled {
                showArrows = false
            }
        }
    }

    private func handleArrowHover(_ hovering: Bool) {
        if hovering {
            // Mouse is over a nav button — keep the arrows visible until it leaves.
            arrowHideTask?.cancel()
            showArrows = true
        } else {
            scheduleArrowHide()
        }
    }

    private func loadCurrentImage() {
        guard let item = appState.selectedItem, item.mediaType == .image else {
            currentImage = nil
            sourceCGImage = nil
            return
        }

        // Use preloaded image if available for instant display
        if let preloaded = appState.popPreloadedImage(for: item) {
            setCurrentImage(preloaded)
            scheduleNextPreload()
            return
        }

        isLoadingMedia = true
        sourceCGImage = nil
        Task(priority: .userInitiated) {
            guard let source = appState.mediaSource else { return }
            do {
                let image = try await source.loadImage(for: item)
                setCurrentImage(image)
            } catch {
                self.currentImage = nil
                self.sourceCGImage = nil
            }
            isLoadingMedia = false
            scheduleNextPreload()
        }
    }

    private func setCurrentImage(_ image: NSImage) {
        self.currentImage = image
        if let cgImage = image.cgImage(forProposedRect: nil, context: nil, hints: nil) {
            self.sourceCGImage = cgImage
        } else if let tiffData = image.tiffRepresentation,
                  let bitmap = NSBitmapImageRep(data: tiffData),
                  let cgImage = bitmap.cgImage {
            self.sourceCGImage = cgImage
        }
    }

    private func scheduleNextPreload() {
        guard let next = appState.nextItem else { return }
        appState.prefetch(next)
    }

    private func updateEditedImage() {
        guard let sourceCG = sourceCGImage else { return }
        guard isEditing else { editedImage = nil; return }
        editedImage = pipeline.render(source: sourceCG, includeCrop: !isCropping)
    }

    private func resetEditingState() {
        isEditing = false
        editMode = .adjust
        cropAspect = .free
        pipeline.reset()
        editedImage = nil
    }

    private func save(_ work: @escaping () async -> Void) {
        isSaving = true
        Task {
            await work()
            isSaving = false
        }
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
