package vault

import (
	"path/filepath"
	"strings"
)

// MediaType classifies a file for the browser UI.
type MediaType string

const (
	// MediaOther is anything the viewer cannot display.
	MediaOther MediaType = ""
	// MediaImage is a still image.
	MediaImage MediaType = "image"
	// MediaVideo is a video.
	MediaVideo MediaType = "video"
)

// imageExtensions and videoExtensions mirror
// ImageThat/Utilities/SupportedFormats.swift so the web browser and the macOS
// app classify the same file the same way.
var imageExtensions = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "heic": true, "heif": true,
	"tiff": true, "tif": true, "bmp": true, "gif": true, "webp": true,
	"raw": true, "cr2": true, "cr3": true, "nef": true, "arw": true,
	"dng": true, "orf": true, "rw2": true, "pef": true, "srw": true,
	"raf": true,
}

var videoExtensions = map[string]bool{
	"mp4": true, "mov": true, "m4v": true, "avi": true, "mkv": true,
	"webm": true,
}

// Extension returns a file's lowercased extension without the dot.
func Extension(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// ClassifyMedia reports how a file should be presented.
//
// Note this differs deliberately from the macOS app, which treats every
// non-video as an image. Here anything unrecognised is MediaOther so the web
// UI can show a generic file rather than a broken <img>.
func ClassifyMedia(name string) MediaType {
	ext := Extension(name)
	switch {
	case videoExtensions[ext]:
		return MediaVideo
	case imageExtensions[ext]:
		return MediaImage
	default:
		return MediaOther
	}
}

// browserDecodableImages are the still formats a browser renders natively.
// HEIC/HEIF and the raw formats are listed as images but need a decoder the
// server does not have, so thumbnails for them are skipped.
var browserDecodableImages = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true,
}

// IsBrowserDecodableImage reports whether a browser can render the file
// directly from its bytes.
func IsBrowserDecodableImage(name string) bool {
	return browserDecodableImages[Extension(name)]
}

// ContentType returns a MIME type for a vault file, used for Content-Type on
// media responses. It intentionally avoids sniffing, which would require
// decrypting a prefix of every file.
func ContentType(name string) string {
	switch Extension(name) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	case "tif", "tiff":
		return "image/tiff"
	case "heic":
		return "image/heic"
	case "heif":
		return "image/heif"
	case "dng":
		return "image/x-adobe-dng"
	case "cr2", "cr3", "nef", "arw", "orf", "rw2", "pef", "srw", "raf", "raw":
		return "image/x-dcraw"
	case "mp4", "m4v":
		return "video/mp4"
	case "mov":
		return "video/quicktime"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	case "avi":
		return "video/x-msvideo"
	default:
		return "application/octet-stream"
	}
}
