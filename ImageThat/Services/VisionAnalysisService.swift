import Foundation
import Vision
import CoreGraphics

actor VisionAnalysisService {
    /// Classifies the image and returns top labels with confidence > 0.3.
    func classifyImage(_ cgImage: CGImage) async throws -> [String] {
        let request = VNClassifyImageRequest()

        let handler = VNImageRequestHandler(cgImage: cgImage, options: [:])
        try handler.perform([request])

        guard let results = request.results else {
            return []
        }

        return results
            .compactMap { $0 as? VNClassificationObservation }
            .filter { $0.confidence > 0.3 }
            .prefix(10)
            .map { $0.identifier }
    }

    /// Recognizes text in the image and returns all recognized text concatenated.
    func recognizeText(in cgImage: CGImage) async throws -> String {
        let request = VNRecognizeTextRequest()
        request.recognitionLanguages = ["en-US"]
        request.recognitionLevel = .accurate

        let handler = VNImageRequestHandler(cgImage: cgImage, options: [:])
        try handler.perform([request])

        guard let results = request.results else {
            return ""
        }

        let textBlocks = results.compactMap { observation -> String? in
            (observation as? VNRecognizedTextObservation)?.topCandidates(1).first?.string
        }

        return textBlocks.joined(separator: "\n")
    }
}
