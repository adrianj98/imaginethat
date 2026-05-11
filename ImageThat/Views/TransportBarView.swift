import SwiftUI
import AVKit

public struct TransportBarView: View {
    var player: AVPlayer
    @Binding var currentTime: Double
    @Binding var duration: Double
    @State private var isScrubbing = false
    @State private var scrubPosition: Double = 0

    public init(player: AVPlayer, currentTime: Binding<Double>, duration: Binding<Double>) {
        self.player = player
        self._currentTime = currentTime
        self._duration = duration
    }

    public var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                // Skip back 10s
                Button(action: { seekBy(-10) }) {
                    Image(systemName: "gobackward.10")
                        .font(.system(size: 14))
                        .foregroundStyle(.white)
                        .frame(width: 24, height: 24)
                }
                .buttonStyle(.plain)
                .help("Back 10 seconds")

                // Play/pause button
                Button(action: togglePlayPause) {
                    Image(systemName: player.rate > 0 ? "pause.fill" : "play.fill")
                        .font(.system(size: 12))
                        .foregroundStyle(.white)
                        .frame(width: 24, height: 24)
                }
                .buttonStyle(.plain)

                // Skip forward 10s
                Button(action: { seekBy(10) }) {
                    Image(systemName: "goforward.10")
                        .font(.system(size: 14))
                        .foregroundStyle(.white)
                        .frame(width: 24, height: 24)
                }
                .buttonStyle(.plain)
                .help("Forward 10 seconds")

                // Scrubber slider - only seek on explicit user drag
                Slider(
                    value: Binding(
                        get: { isScrubbing ? scrubPosition : currentTime },
                        set: { newValue in
                            if isScrubbing { scrubPosition = newValue }
                        }
                    ),
                    in: 0...max(duration, 0.1)
                ) { editing in
                    if editing {
                        isScrubbing = true
                        scrubPosition = currentTime
                    } else {
                        isScrubbing = false
                        player.seek(to: CMTime(seconds: scrubPosition, preferredTimescale: 1000))
                        currentTime = scrubPosition
                    }
                }

                // Time display
                Text("\(formatTime(currentTime)) / \(formatTime(duration))")
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.white)
                    .frame(minWidth: 60)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)
        }
        .frame(height: 32)
        .background(Color.black.opacity(0.5))
    }

    private func togglePlayPause() {
        if player.rate > 0 {
            player.pause()
        } else {
            player.play()
        }
    }

    private func seekBy(_ delta: Double) {
        let target = max(0, min(duration, currentTime + delta))
        player.seek(to: CMTime(seconds: target, preferredTimescale: 1000))
        currentTime = target
    }

    private func formatTime(_ seconds: Double) -> String {
        guard !seconds.isNaN && !seconds.isInfinite else { return "0:00" }
        let totalSeconds = Int(seconds)
        let mins = totalSeconds / 60
        let secs = totalSeconds % 60
        return String(format: "%d:%02d", mins, secs)
    }
}
