import Foundation
import UniformTypeIdentifiers
import CoreTransferable

public struct MediaTransfer: Transferable, Codable, Sendable {
    /// Vault root absolute path OR parent folder absolute path
    public let sourceRootPath: String
    /// Vault-relative path OR filename (for local items)
    public let itemRelativePath: String
    /// True if source is a vault, false if local folder
    public let isVault: Bool

    public init(sourceRootPath: String, itemRelativePath: String, isVault: Bool) {
        self.sourceRootPath = sourceRootPath
        self.itemRelativePath = itemRelativePath
        self.isVault = isVault
    }

    public static var transferRepresentation: some TransferRepresentation {
        CodableRepresentation(contentType: .imageThatTransfer)
    }
}

extension MediaTransfer {
    /// Create a MediaTransfer for dragging a media item from its current source
    public init?(for item: MediaItem, source: (any MediaSource)?) {
        if let v = source as? VaultSource {
            self.init(sourceRootPath: v.bridge.vaultPath.path, itemRelativePath: item.url.path, isVault: true)
        } else if let f = source as? FolderSource {
            self.init(sourceRootPath: f.rootURL.path, itemRelativePath: item.name, isVault: false)
        } else {
            return nil
        }
    }
}

extension UTType {
    public static let imageThatTransfer = UTType(exportedAs: "com.adrianm.imagethat.transfer")
}
