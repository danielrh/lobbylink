// swift-tools-version:5.10
// Language mode 5: libdatachannel callbacks cross threads through
// Unmanaged pointers, which strict Swift 6 concurrency cannot model;
// the client protects its state with locks instead.
import PackageDescription

let package = Package(
    name: "lobbylink-client",
    products: [
        .library(name: "LobbyLink", targets: ["LobbyLink"]),
        .executable(name: "lobbylink-chat", targets: ["lobbylink-chat"]),
    ],
    targets: [
        // libdatachannel's C API, expected under /usr/local (see README).
        .systemLibrary(name: "CDataChannel", path: "Sources/CDataChannel"),
        .target(
            name: "LobbyLink",
            dependencies: ["CDataChannel"],
            linkerSettings: [
                .linkedLibrary("datachannel"),
                // libdatachannel has no pkg-config file, so the library
                // path is spelled out; unsafeFlags means this package
                // must be consumed as a local/branch dependency.
                .unsafeFlags(["-L/usr/local/lib"]),
            ]
        ),
        .executableTarget(name: "lobbylink-chat", dependencies: ["LobbyLink"]),
        .testTarget(name: "LobbyLinkTests", dependencies: ["LobbyLink"]),
    ]
)
