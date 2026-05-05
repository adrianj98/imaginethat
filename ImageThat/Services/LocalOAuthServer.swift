import Network
import Foundation

/// One-shot local HTTP server that listens on a random OS-assigned port.
/// Used to catch the OAuth 2.0 redirect from Google without requiring a
/// registered custom URL scheme.
final class LocalOAuthServer: @unchecked Sendable {
    private var listener: NWListener?
    private var continuation: CheckedContinuation<URL, Error>?
    private let lock = NSLock()

    /// Starts listening and returns the OS-assigned port number.
    func start() async throws -> UInt16 {
        let listener = try NWListener(using: .tcp)
        self.listener = listener

        return try await withCheckedThrowingContinuation { cont in
            var portReturned = false

            listener.stateUpdateHandler = { [weak listener] state in
                switch state {
                case .ready:
                    guard !portReturned, let port = listener?.port?.rawValue, port > 0 else { return }
                    portReturned = true
                    cont.resume(returning: port)
                case .failed(let error):
                    guard !portReturned else { return }
                    portReturned = true
                    cont.resume(throwing: error)
                default:
                    break
                }
            }

            listener.newConnectionHandler = { [weak self] connection in
                self?.handleConnection(connection)
            }

            listener.start(queue: .global(qos: .userInteractive))
        }
    }

    /// Waits for the OAuth callback URL, or throws after the given timeout.
    func waitForCallback(timeout: TimeInterval) async throws -> URL {
        return try await withCheckedThrowingContinuation { cont in
            lock.lock()
            continuation = cont
            lock.unlock()

            Task {
                try? await Task.sleep(for: .seconds(timeout))
                lock.lock()
                let pending = continuation
                continuation = nil
                lock.unlock()
                pending?.resume(throwing: GoogleDriveError.authFailed("Authentication timed out. Please try again."))
                self.stop()
            }
        }
    }

    func stop() {
        listener?.cancel()
        listener = nil
    }

    // MARK: - Connection handler

    private func handleConnection(_ connection: NWConnection) {
        connection.start(queue: .global(qos: .userInteractive))
        connection.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] data, _, _, _ in
            guard let self else { connection.cancel(); return }

            guard let data,
                  let request = String(data: data, encoding: .utf8) else {
                connection.cancel()
                return
            }

            // Parse: "GET /oauth?code=...&state=... HTTP/1.1"
            let firstLine = request.components(separatedBy: "\r\n").first ?? ""
            let parts = firstLine.components(separatedBy: " ")
            guard parts.count >= 2, parts[0] == "GET",
                  let callbackURL = URL(string: "http://localhost\(parts[1])") else {
                connection.cancel()
                return
            }

            // Favicon / pre-flight requests — ignore
            guard callbackURL.path.hasPrefix("/oauth") else {
                connection.cancel()
                return
            }

            self.sendSuccessResponse(to: connection)

            self.lock.lock()
            let pending = self.continuation
            self.continuation = nil
            self.lock.unlock()

            pending?.resume(returning: callbackURL)
            self.stop()
        }
    }

    private func sendSuccessResponse(to connection: NWConnection) {
        let html = """
        <!DOCTYPE html>
        <html>
        <head>
        <title>Google Drive Connected</title>
        <style>
        body { font-family: -apple-system, BlinkMacSystemFont, sans-serif;
               max-width: 400px; margin: 80px auto; text-align: center; color: #1d1d1f; }
        h2 { font-weight: 600; }
        p  { color: #6e6e73; }
        </style>
        </head>
        <body>
        <h2>✓ Google Drive Connected</h2>
        <p>You can close this tab and return to ImageThat.</p>
        </body>
        </html>
        """
        let body = Data(html.utf8)
        let header = "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n"
        var response = Data(header.utf8)
        response.append(body)
        connection.send(content: response, completion: .contentProcessed { _ in
            connection.cancel()
        })
    }
}
