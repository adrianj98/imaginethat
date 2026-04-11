import SwiftUI
import AVKit

public struct DetailView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    @State private var currentImage: NSImage?
    @State private var isLoadingMedia = false
    @GestureState private var dragOffset: CGFloat = 0

    public init() {}

    public var body: some View {
        GeometryReader { geo in
            ZStack {
                Color(nsColor: .windowBackgroundColor).ignoresSafeArea()

                if let item = appState.selectedItem {
                    if item.mediaType == .video {
                        VideoPlayerView(item: item)
                    } else {
                        imageView
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
            .gesture(swipeGesture)
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
            loadCurrentImage()
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
        } else if let image = currentImage {
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
            return
        }
        isLoadingMedia = true
        Task {
            guard let source = appState.mediaSource else { return }
            do {
                let image = try await source.loadImage(for: item)
                self.currentImage = image
            } catch {
                self.currentImage = nil
            }
            isLoadingMedia = false
        }
    }
}
