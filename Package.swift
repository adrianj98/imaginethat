// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "ImageThat",
    platforms: [
        .macOS(.v14)
    ],
    targets: [
        .systemLibrary(
            name: "CLibGocryptfs",
            path: "CLibGocryptfs"
        ),
        .target(
            name: "ImageThatCore",
            dependencies: ["CLibGocryptfs"],
            path: "ImageThat",
            exclude: ["BridgingHeader.h", "App/ImageThatApp.swift"],
            resources: [
                .process("Resources/Assets.xcassets")
            ],
            linkerSettings: [
                .unsafeFlags(["-L\(Context.packageDirectory)/build"]),
                .linkedLibrary("gocryptfs")
            ]
        ),
        .executableTarget(
            name: "ImageThat",
            dependencies: ["ImageThatCore", "CLibGocryptfs"],
            path: "ImageThatApp",
            linkerSettings: [
                .unsafeFlags(["-L\(Context.packageDirectory)/build"]),
                .linkedLibrary("gocryptfs")
            ]
        ),
        .executableTarget(
            name: "IntegrationTest",
            dependencies: ["ImageThatCore", "CLibGocryptfs"],
            path: "Tests",
            exclude: ["e2e_test.sh", "fixtures"],
            linkerSettings: [
                .unsafeFlags(["-L\(Context.packageDirectory)/build"]),
                .linkedLibrary("gocryptfs")
            ]
        )
    ]
)
