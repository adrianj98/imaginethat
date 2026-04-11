import Foundation

public enum SupportedFormats {
    public static let imageExtensions: Set<String> = [
        "jpg", "jpeg", "png", "heic", "heif", "tiff", "tif",
        "bmp", "gif", "webp", "raw", "cr2", "cr3", "nef",
        "arw", "dng", "orf", "rw2", "pef", "srw", "raf"
    ]

    public static let videoExtensions: Set<String> = [
        "mp4", "mov", "m4v", "avi", "mkv", "webm"
    ]

    public static let allExtensions: Set<String> = imageExtensions.union(videoExtensions)
}
