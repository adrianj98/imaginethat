import SwiftUI
import AVKit

public struct VideoPlayerView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    public let item: MediaItem
    @State private var player: AVPlayer?
    @State private var showControls = true
    @State private var hideTask: Task<Void, Never>?
    @State private var currentTime: Double = 0
    @State private var duration: Double = 0
    @State private var cachedRanges: [ClosedRange<Double>] = []
    @State private var bufferedRanges: [ClosedRange<Double>] = []
    @State private var cacheTask: Task<Void, Never>?

    public init(item: MediaItem) {
        self.item = item
    }

    public var body: some View {
        Group {
            if let player {
                ZStack(alignment: .bottom) {
                    VideoPlayerRepresentable(player: player)

                    // Mouse tracking overlay for more reliable detection
                    MouseTrackingView(
                        onMouseMoved: {
                            showControls = true
                            resetHideTimer()
                        },
                        onMouseExited: {
                            scheduleHide()
                        }
                    )
                    .allowsHitTesting(false)

                    // Transport bar
                    TransportBarView(player: player, currentTime: $currentTime, duration: $duration,
                                     cached: cachedRanges, buffered: bufferedRanges)
                        .opacity(showControls ? 1 : 0)
                        .animation(.easeOut(duration: 0.25), value: showControls)
                }
            } else {
                ProgressView()
                    .progressViewStyle(.circular)
            }
        }
        .onAppear {
            loadVideo()
        }
        .onDisappear {
            player?.pause()
            player = nil
            hideTask?.cancel()
            cacheTask?.cancel()
        }
        .onChange(of: item) { _, newItem in
            player?.pause()
            hideTask?.cancel()
            cachedRanges = []
            bufferedRanges = []
            loadVideo()
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(phases: .down) { keyPress in
            if shortcuts.matches(keyPress, action: .togglePlayPause) {
                togglePlayPause()
                return .handled
            }
            return .ignored
        }
    }

    private func loadVideo() {
        guard let source = appState.mediaSource else { return }

        do {
            let playerItem = try source.createPlayerItem(for: item)
            let avPlayer = AVPlayer(playerItem: playerItem)
            setupTimeObserver(for: avPlayer)
            self.player = avPlayer
            avPlayer.play()
            watchCache(source: source)
        } catch {
            self.player = nil
        }
    }

    private func setupTimeObserver(for player: AVPlayer) {
        let interval = CMTime(seconds: 0.5, preferredTimescale: 1000)
        player.addPeriodicTimeObserver(forInterval: interval, queue: .main) { [weak player] time in
            guard let player = player else { return }
            self.currentTime = time.seconds
            if let item = player.currentItem {
                let duration = item.duration.seconds
                if !duration.isNaN && !duration.isInfinite && self.duration != duration {
                    self.duration = duration
                }
                if duration.isFinite, duration > 0 {
                    self.bufferedRanges = item.loadedTimeRanges.map { value in
                        let r = value.timeRangeValue
                        let start = max(0, r.start.seconds / duration)
                        return start...min(1, max(start, (r.start.seconds + r.duration.seconds) / duration))
                    }
                }
            }
        }
    }

    /// Refreshes the on-disk cache ranges while the video is open: they grow
    /// as playback and seeks fetch new windows.
    private func watchCache(source: any MediaSource) {
        cacheTask?.cancel()
        let item = self.item
        cacheTask = Task {
            while !Task.isCancelled {
                let ranges = await source.cachedRanges(for: item)
                if Task.isCancelled { return }
                cachedRanges = ranges
                try? await Task.sleep(for: .seconds(2))
            }
        }
    }

    private func resetHideTimer() {
        hideTask?.cancel()
        hideTask = Task {
            try? await Task.sleep(nanoseconds: 3_000_000_000) // 3 seconds
            if !Task.isCancelled {
                showControls = false
            }
        }
    }

    private func scheduleHide() {
        hideTask?.cancel()
        hideTask = Task {
            try? await Task.sleep(nanoseconds: 3_000_000_000) // 3 seconds
            if !Task.isCancelled {
                showControls = false
            }
        }
    }

    private func togglePlayPause() {
        guard let player else { return }
        if player.rate > 0 {
            player.pause()
        } else {
            player.play()
        }
    }
}

public struct VideoPlayerRepresentable: NSViewRepresentable {
    public let player: AVPlayer

    public init(player: AVPlayer) {
        self.player = player
    }

    public func makeNSView(context: Context) -> PlayerLayerView {
        let view = PlayerLayerView()
        view.player = player
        return view
    }

    public func updateNSView(_ nsView: PlayerLayerView, context: Context) {
        nsView.player = player
    }
}

/// `AVPlayerLayer` host that uses the standard window background color outside the
/// video frame, instead of `AVPlayerView`'s opaque black letterbox.
public final class PlayerLayerView: NSView {
    private let playerLayer = AVPlayerLayer()

    public override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        configure()
    }

    @available(*, unavailable)
    public required init?(coder: NSCoder) { fatalError() }

    private func configure() {
        wantsLayer = true
        let host = CALayer()
        host.backgroundColor = nil
        host.isOpaque = false
        layer = host
        playerLayer.videoGravity = .resizeAspect
        playerLayer.backgroundColor = nil
        playerLayer.isOpaque = false
        host.addSublayer(playerLayer)
    }

    public override var isOpaque: Bool { false }

    public var player: AVPlayer? {
        get { playerLayer.player }
        set { playerLayer.player = newValue }
    }

    public override func layout() {
        super.layout()
        playerLayer.frame = bounds
    }
}

struct MouseTrackingView: NSViewRepresentable {
    var onMouseMoved: () -> Void
    var onMouseExited: () -> Void

    func makeNSView(context: Context) -> NSView {
        let view = TrackingView(onMouseMoved: onMouseMoved, onMouseExited: onMouseExited)
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {}
}

class TrackingView: NSView {
    var onMouseMoved: () -> Void
    var onMouseExited: () -> Void
    var trackingArea: NSTrackingArea?

    init(onMouseMoved: @escaping () -> Void, onMouseExited: @escaping () -> Void) {
        self.onMouseMoved = onMouseMoved
        self.onMouseExited = onMouseExited
        super.init(frame: .zero)
        setupTracking()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    private func setupTracking() {
        let trackingArea = NSTrackingArea(
            rect: bounds,
            options: [.mouseMoved, .mouseEnteredAndExited, .inVisibleRect, .activeInKeyWindow],
            owner: self,
            userInfo: nil
        )
        self.trackingArea = trackingArea
        addTrackingArea(trackingArea)
    }

    override func updateTrackingAreas() {
        if let trackingArea = trackingArea {
            removeTrackingArea(trackingArea)
        }
        setupTracking()
    }

    override func mouseMoved(with event: NSEvent) {
        onMouseMoved()
    }

    override func mouseExited(with event: NSEvent) {
        onMouseExited()
    }
}
