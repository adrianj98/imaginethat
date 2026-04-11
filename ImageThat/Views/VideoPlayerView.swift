import SwiftUI
import AVKit

public struct VideoPlayerView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    public let item: MediaItem
    @State private var player: AVPlayer?

    public init(item: MediaItem) {
        self.item = item
    }

    public var body: some View {
        Group {
            if let player {
                VideoPlayerRepresentable(player: player)
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
        }
        .onChange(of: item) { _, newItem in
            player?.pause()
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

        // For encrypted vaults, use streaming resource loader (zero disk writes)
        if let vaultSource = source as? VaultSource {
            do {
                let playerItem = try vaultSource.createPlayerItem(for: item)
                let avPlayer = AVPlayer(playerItem: playerItem)
                self.player = avPlayer
                avPlayer.play()
            } catch {
                self.player = nil
            }
            return
        }

        // For plain folders, use file URL directly
        Task {
            do {
                let url = try await source.videoURL(for: item)
                let avPlayer = AVPlayer(url: url)
                self.player = avPlayer
                avPlayer.play()
            } catch {
                self.player = nil
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

    public func makeNSView(context: Context) -> AVPlayerView {
        let view = AVPlayerView()
        view.player = player
        view.controlsStyle = .floating
        view.showsFullScreenToggleButton = true
        return view
    }

    public func updateNSView(_ nsView: AVPlayerView, context: Context) {
        nsView.player = player
    }
}
