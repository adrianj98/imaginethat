import SwiftUI
import AVKit

public struct TransportBarView: View {
    var player: AVPlayer
    @Binding var currentTime: Double
    @Binding var duration: Double
    @State private var lastSeekTime: Double = 0

    public init(player: AVPlayer, currentTime: Binding<Double>, duration: Binding<Double>) {
        self.player = player
        self._currentTime = currentTime
        self._duration = duration
    }

    public var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                // Play/pause button
                Button(action: togglePlayPause) {
                    Image(systemName: player.rate > 0 ? "pause.fill" : "play.fill")
                        .font(.system(size: 12))
                        .foregroundStyle(.white)
                        .frame(width: 24, height: 24)
                }
                .buttonStyle(.plain)

                // Scrubber slider - detect large jumps as seeks
                Slider(value: $currentTime, in: 0...max(duration, 0.1))
                    .onChange(of: currentTime) { oldValue, newValue in
                        // If jump is > 0.5 seconds, it's likely a user-initiated seek
                        if abs(newValue - oldValue) > 0.5 {
                            player.seek(to: CMTime(seconds: newValue, preferredTimescale: 1000))
                            lastSeekTime = newValue
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

    private func formatTime(_ seconds: Double) -> String {
        guard !seconds.isNaN && !seconds.isInfinite else { return "0:00" }
        let totalSeconds = Int(seconds)
        let mins = totalSeconds / 60
        let secs = totalSeconds % 60
        return String(format: "%d:%02d", mins, secs)
    }
}
