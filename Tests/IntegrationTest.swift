import Foundation
import AppKit
import AVFoundation
import ImageThatCore

@main
struct IntegrationTest {
    static func main() async {
        var passed = 0
        var failed = 0

        func check(_ name: String, _ condition: Bool, _ detail: String = "") {
            if condition {
                print("  PASS: \(name)")
                passed += 1
            } else {
                print("  FAIL: \(name) \(detail)")
                failed += 1
            }
        }

        // === Test 1: FolderSource ===
        print("\n--- FolderSource Tests ---")

        let plainDir = URL(fileURLWithPath: "/tmp/imagethat-test-plain")
        let folderSource = FolderSource(rootURL: plainDir)

        do {
            let items = try await folderSource.listMedia()
            check("listMedia returns items", items.count == 3, "got \(items.count)")
            check("all items are images", items.allSatisfy { $0.mediaType == .image })
            check("names are correct", Set(items.map(\.name)) == Set(["red.png", "green.png", "blue.png"]),
                  "got \(items.map(\.name))")

            // Test image loading
            if let redItem = items.first(where: { $0.name == "red.png" }) {
                let image = try await folderSource.loadImage(for: redItem)
                check("loadImage returns NSImage", image.size.width > 0, "size=\(image.size)")

                let thumb = try await folderSource.loadThumbnail(for: redItem, size: CGSize(width: 100, height: 100))
                check("loadThumbnail returns NSImage", thumb.size.width > 0)
            } else {
                check("find red.png", false)
            }
        } catch {
            check("FolderSource operations", false, error.localizedDescription)
        }

        // === Test 2: GocryptfsBridge ===
        print("\n--- GocryptfsBridge Tests ---")

        let vaultDir = URL(fileURLWithPath: "/tmp/imagethat-test/vault")

        // Test wrong password
        do {
            _ = try GocryptfsBridge(vaultPath: vaultDir, password: "wrongpassword")
            check("wrong password rejected", false, "should have thrown")
        } catch let error as GocryptfsError {
            check("wrong password rejected",
                  error.errorDescription?.contains("Wrong") == true || "\(error)" == "wrongPassword",
                  "got: \(error) / \(error.errorDescription ?? "nil")")
        } catch {
            check("wrong password rejected", false, "unexpected error: \(error)")
        }

        // Test correct password
        do {
            let bridge = try GocryptfsBridge(vaultPath: vaultDir, password: "testpass123")
            check("vault opens with correct password", true)

            // List directory
            let entries = try bridge.listDirectory()
            check("listDirectory returns entries", entries.count == 2, "got \(entries.count)")

            let names = Set(entries.map(\.name))
            check("decrypted filenames correct", names == Set(["test_image.png", "hello.txt"]),
                  "got \(names)")

            // Check file sizes
            if let txtEntry = entries.first(where: { $0.name == "hello.txt" }) {
                check("hello.txt size correct", txtEntry.size == 27, "got \(txtEntry.size)")
            }

            // Decrypt text file
            let txtData = try bridge.decryptFile(path: "hello.txt")
            let txtContent = String(data: txtData, encoding: .utf8) ?? ""
            check("decrypt hello.txt content", txtContent == "Hello from encrypted vault\n",
                  "got '\(txtContent)'")

            // Decrypt image file
            let imgData = try bridge.decryptFile(path: "test_image.png")
            check("decrypt test_image.png non-empty", imgData.count > 0, "got \(imgData.count) bytes")

            // Verify it's a valid PNG
            let isPNG = imgData.count > 8 && imgData[0] == 0x89 && imgData[1] == 0x50 // \x89P
            check("decrypted data is valid PNG", isPNG)

            // Load as NSImage
            let image = NSImage(data: imgData)
            check("decrypted PNG loads as NSImage", image != nil && (image?.size.width ?? 0) > 0,
                  "image=\(String(describing: image))")

            // Test fileSize
            let size = try bridge.fileSize(path: "hello.txt")
            check("fileSize returns correct value", size == 27, "got \(size)")

            // Test readRange
            let rangeData = try bridge.readRange(path: "hello.txt", offset: 0, length: 5)
            let rangeStr = String(data: rangeData, encoding: .utf8) ?? ""
            check("readRange returns correct data", rangeStr == "Hello", "got '\(rangeStr)'")

            // Test readRange with offset
            let rangeData2 = try bridge.readRange(path: "hello.txt", offset: 6, length: 4)
            let rangeStr2 = String(data: rangeData2, encoding: .utf8) ?? ""
            check("readRange with offset", rangeStr2 == "from", "got '\(rangeStr2)'")

        } catch {
            check("GocryptfsBridge operations", false, error.localizedDescription)
        }

        // === Test 3: VaultSource ===
        print("\n--- VaultSource Tests ---")

        do {
            let bridge = try GocryptfsBridge(vaultPath: vaultDir, password: "testpass123")
            let vaultSource = VaultSource(bridge: bridge)

            let items = try await vaultSource.listMedia()
            check("VaultSource lists media", items.count == 1, "got \(items.count) (only image, txt filtered)")

            if let imgItem = items.first {
                check("VaultSource item is test_image.png", imgItem.name == "test_image.png")

                let image = try await vaultSource.loadImage(for: imgItem)
                check("VaultSource loadImage works", image.size.width > 0)

                let thumb = try await vaultSource.loadThumbnail(for: imgItem, size: CGSize(width: 50, height: 50))
                check("VaultSource loadThumbnail works", thumb.size.width > 0)
            }
        } catch {
            check("VaultSource operations", false, error.localizedDescription)
        }

        // === Summary ===
        print("\n=== Results: \(passed) passed, \(failed) failed ===")
        if failed > 0 {
            exit(1)
        }
    }
}
