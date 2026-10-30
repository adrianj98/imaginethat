import SwiftUI
import AVKit

public struct TransportBarView: View {
    var player: AVPlayer
    @Binding var currentTime: Double
    @Binding var duration: Double
    /// Parts of the file in a persistent local cache, as fractions (0...1).
    var cached: [ClosedRange<Double>]
    /// Parts the player has loaded into memory, as fractions (0...1).
    var buffered: [ClosedRange<Double>]
    /// Playback is stalled waiting for data.
    var isBuffering: Bool
    /// Seconds loaded past the playhead, or nil before anything is loaded.
    var bufferedAhead: Double?
    @State private var isScrubbing = false
    @State private var scrubPosition: Double = 0

    public init(player: AVPlayer, currentTime: Binding<Double>, duration: Binding<Double>,
                cached: [ClosedRange<Double>] = [], buffered: [ClosedRange<Double>] = [],
                isBuffering: Bool = false, bufferedAhead: Double? = nil) {
        self.player = player
        self._currentTime = currentTime
        self._duration = duration
        self.cached = cached
        self.buffered = buffered
        self.isBuffering = isBuffering
        self.bufferedAhead = bufferedAhead
    }

    public var body: some View {
        HStack(spacing: 8) {
            Button(action: { seekBy(-10) }) {
                Image(systemName: "gobackward.10")
                    .font(.system(size: 16))
                    .frame(width: 34, height: 34)
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .help("Back 10 seconds")

            Button(action: togglePlayPause) {
                Image(systemName: player.rate > 0 ? "pause.fill" : "play.fill")
                    .font(.system(size: 13))
                    .foregroundStyle(Glass.darkCanvas)
                    .frame(width: 36, height: 36)
                    .background(Circle().fill(Color.white.opacity(0.96)))
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .help(player.rate > 0 ? "Pause" : "Play")

            Button(action: { seekBy(10) }) {
                Image(systemName: "goforward.10")
                    .font(.system(size: 16))
                    .frame(width: 34, height: 34)
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .help("Forward 10 seconds")

            Text(formatTime(isScrubbing ? scrubPosition : currentTime))
                .monospacedDigit()
                .foregroundStyle(Color.white.opacity(0.82))
                .padding(.leading, 4)

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
            .tint(.white)
            .controlSize(.small)
            .frame(width: 260)
            .overlay(alignment: .bottom) {
                RangeStrip(cached: cached, buffered: buffered)
                    .frame(height: 3)
                    .padding(.horizontal, 2)
                    .offset(y: 5)
                    .allowsHitTesting(false)
                    .help("Bright: cached on disk · Faint: loaded in memory")
            }

            Text(formatTime(duration))
                .monospacedDigit()
                .foregroundStyle(Glass.label)

            bufferStatus
                .padding(.trailing, 6)
        }
        .font(.system(size: 12))
        .foregroundStyle(.white)
        .padding(.leading, 10)
        .padding(.trailing, 8)
        .frame(height: 48)
        .glassCard(dark: true, radius: 24)
        .padding(.bottom, 20)
    }

    /// Small status chip: a spinner while stalled, otherwise how much is
    /// loaded ahead of the playhead.
    @ViewBuilder private var bufferStatus: some View {
        let toEnd = bufferedAhead.map { duration > 0 && currentTime + $0 >= duration - 0.5 } ?? false
        HStack(spacing: 4) {
            if isBuffering {
                ProgressView()
                    .controlSize(.mini)
                    .tint(.white)
                Text("Buffering")
            } else if toEnd {
                Image(systemName: "checkmark.circle")
                Text("Loaded")
            } else if let ahead = bufferedAhead {
                Image(systemName: ahead < 5 ? "exclamationmark.circle" : "arrow.down.circle")
                Text("\(Int(ahead))s")
                    .monospacedDigit()
            } else {
                Image(systemName: "arrow.down.circle")
                Text("—")
            }
        }
        .font(.system(size: 11))
        .foregroundStyle(isBuffering || (!toEnd && (bufferedAhead.map { $0 < 5 } ?? false)) ? Glass.edited : Color.white.opacity(0.7))
        .frame(minWidth: 64, alignment: .leading)
        .help(bufferHelp(toEnd: toEnd))
        .accessibilityElement(children: .combine)
        .accessibilityLabel(bufferHelp(toEnd: toEnd))
    }

    private func bufferHelp(toEnd: Bool) -> String {
        if isBuffering { return "Buffering: waiting for data" }
        if toEnd { return "Loaded to the end" }
        if let ahead = bufferedAhead { return "Buffered \(Int(ahead)) seconds ahead" }
        return "Nothing buffered yet"
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

    /// Draws ranges given as fractions of the timeline.
    private struct RangeStrip: View {
        let cached: [ClosedRange<Double>]
        let buffered: [ClosedRange<Double>]

        var body: some View {
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.white.opacity(0.12))
                    segments(buffered, width: geo.size.width, color: .white.opacity(0.28))
                    segments(cached, width: geo.size.width, color: .white.opacity(0.6))
                }
            }
        }

        private func segments(_ ranges: [ClosedRange<Double>], width: CGFloat, color: Color) -> some View {
            ZStack(alignment: .leading) {
                ForEach(Array(ranges.enumerated()), id: \.offset) { _, r in
                    Rectangle()
                        .fill(color)
                        .frame(width: max(1, CGFloat(r.upperBound - r.lowerBound) * width))
                        .offset(x: CGFloat(r.lowerBound) * width)
                }
            }
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
