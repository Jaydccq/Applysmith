import Foundation
import Darwin
import PersonalCUCore

/// Trusted local scripting. Node's permission flags reduce accidental access;
/// neither the REPL context nor those flags are a hostile-code sandbox.
@MainActor
final class JavaScriptBridge {
    private static let maximumBytes = 8 * 1024 * 1024
    private static let methods = Set([
        "list_apps", "list_windows", "get_window", "launch_app", "get_app_state",
        "get_window_state", "activate_window", "click", "type_text", "paste",
        "press_key", "scroll", "set_value", "perform_secondary_action", "select_text",
        "drag", "drag_paths", "debug_tap", "doctor", "activate_app"
    ])
    private final class Execution {
        let id: String
        let continuation: CheckedContinuation<DesktopResult, Error>
        let invoke: @MainActor (String, [String: Any]) async throws -> DesktopResult
        var nativeTask: Task<Void, Never>?
        var images: [Data] = []
        var imageBytes = 0
        var calls = 0
        init(id: String, continuation: CheckedContinuation<DesktopResult, Error>,
             invoke: @escaping @MainActor (String, [String: Any]) async throws -> DesktopResult) {
            self.id = id; self.continuation = continuation; self.invoke = invoke
        }
    }
    private let writer = DispatchQueue(label: "personal-cu.javascript.writer")
    private var process: Process?
    private var input: FileHandle?
    private var generation = UUID()
    private var buffer = Data()
    private var execution: Execution?
    private var deadline: Task<Void, Never>?

    init() {}

    func execute(code: String, timeoutMS: Int,
                 invoke: @escaping @MainActor (String, [String: Any]) async throws -> DesktopResult) async throws -> DesktopResult {
        guard execution == nil else { throw LocalCUError.operation("JavaScript runtime is busy; await the current js call") }
        guard code.utf8.count <= Self.maximumBytes, (1...600_000).contains(timeoutMS) else {
            throw LocalCUError.invalidArgument("JavaScript code must fit 8 MiB and timeout_ms must be 1...600000")
        }
        try Task.checkCancellation()
        try startIfNeeded()
        let id = UUID().uuidString
        return try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                execution = Execution(id: id, continuation: continuation, invoke: invoke)
                deadline = Task { [weak self] in
                    do { try await Task.sleep(nanoseconds: UInt64(timeoutMS) * 1_000_000) }
                    catch { return }
                    guard self?.execution?.id == id else { return }
                    self?.fail("JavaScript execution timed out; runtime state was reset")
                }
                send(["type": "exec", "exec_id": id, "code": code])
            }
        }, onCancel: { [weak self] in
            Task { @MainActor in
                guard self?.execution?.id == id else { return }
                self?.fail("JavaScript execution cancelled; runtime state was reset")
            }
        })
    }

    func reset() { fail("JavaScript runtime reset") }

    private func startIfNeeded() throws {
        if let process, process.isRunning { return }
        // PERSONAL_CU_NODE names a Node.js other than Homebrew's on Apple silicon.
        let node = ProcessInfo.processInfo.environment["PERSONAL_CU_NODE"] ?? "/opt/homebrew/bin/node"
        guard FileManager.default.isExecutableFile(atPath: node) else {
            throw LocalCUError.operation("JavaScript requires an independently installed Node.js at \(node)")
        }
        let appResource = Bundle.main.resourceURL?
            .appendingPathComponent("PersonalCU_PersonalCU.bundle/js-worker.mjs")
        let resource: URL?
        if let appResource, FileManager.default.fileExists(atPath: appResource.path) {
            resource = appResource
        } else {
            resource = Bundle.module.url(forResource: "js-worker", withExtension: "mjs")
        }
        guard let resource else {
            throw LocalCUError.operation("JavaScript worker resource is missing; rebuild PersonalCU")
        }
        let script = resource.resolvingSymlinksInPath()
        let child = Process(), incoming = Pipe(), outgoing = Pipe()
        child.executableURL = URL(fileURLWithPath: node)
        child.arguments = ["--permission", "--allow-fs-read=\(script.path)", "--disable-sigusr1", "--max-old-space-size=256", script.path]
        child.environment = ["PATH": "/opt/homebrew/bin:/usr/bin:/bin", "NO_COLOR": "1"]
        child.standardInput = incoming
        child.standardOutput = outgoing
        child.standardError = FileHandle.nullDevice
        guard fcntl(incoming.fileHandleForWriting.fileDescriptor, F_SETNOSIGPIPE, 1) != -1 else {
            throw LocalCUError.operation("Cannot configure JavaScript input pipe")
        }
        let token = UUID()
        generation = token
        buffer.removeAll(keepingCapacity: false)
        try child.run()
        process = child
        input = incoming.fileHandleForWriting
        let reader = outgoing.fileHandleForReading
        // Blocking pipe reads live on a dedicated queue, never the main actor.
        DispatchQueue(label: "personal-cu.javascript.reader").async { [weak self] in
            defer { try? reader.close() }
            var bytes = [UInt8](repeating: 0, count: 65_536)
            while true {
                let count = bytes.withUnsafeMutableBytes { Darwin.read(reader.fileDescriptor, $0.baseAddress, $0.count) }
                if count < 0, errno == EINTR { continue }
                guard count > 0 else { break }
                let data = Data(bytes.prefix(count))
                // Preserve byte order and apply backpressure without blocking
                // the main actor or queuing an unbounded number of chunks.
                let delivered = DispatchSemaphore(value: 0)
                Task { @MainActor [weak self] in
                    self?.receive(data, generation: token)
                    delivered.signal()
                }
                delivered.wait()
            }
            Task { @MainActor [weak self] in
                guard self?.generation == token else { return }
                self?.fail("JavaScript worker exited; runtime state was reset")
            }
        }
    }

    private func send(_ message: [String: Any]) {
        guard let input else { fail("JavaScript worker is not connected"); return }
        do {
            var data = try JSONSerialization.data(withJSONObject: message)
            guard data.count <= Self.maximumBytes else { throw LocalCUError.operation("JavaScript message exceeds 8 MiB") }
            data.append(10)
            let frame = data
            let token = generation
            writer.async { [weak self] in
                do { try input.write(contentsOf: frame) }
                catch {
                    Task { @MainActor [weak self] in
                        guard self?.generation == token else { return }
                        self?.fail("Cannot write to JavaScript worker; runtime state was reset")
                    }
                }
            }
        } catch { fail(error.localizedDescription) }
    }

    private func receive(_ data: Data, generation token: UUID) {
        guard generation == token else { return }
        buffer.append(data)
        do {
            while let newline = buffer.firstIndex(of: 10) {
                let count = buffer.distance(from: buffer.startIndex, to: newline)
                guard count <= Self.maximumBytes else { throw LocalCUError.operation("JavaScript output exceeds 8 MiB") }
                let line = Data(buffer.prefix(count))
                buffer.removeSubrange(buffer.startIndex...newline)
                guard let message = try JSONSerialization.jsonObject(with: line) as? [String: Any] else {
                    throw LocalCUError.operation("Invalid JavaScript worker response")
                }
                try handle(message)
                if generation != token { return }
            }
            guard buffer.count <= Self.maximumBytes else { throw LocalCUError.operation("JavaScript output exceeds 8 MiB") }
        } catch { fail(error.localizedDescription) }
    }

    private func handle(_ message: [String: Any]) throws {
        if message["type"] as? String == "fatal" { throw LocalCUError.operation(message["error"] as? String ?? "JavaScript worker failed") }
        guard let execution, message["exec_id"] as? String == execution.id else {
            throw LocalCUError.operation("JavaScript response has no matching execution")
        }
        switch message["type"] as? String {
        case "call":
            guard execution.nativeTask == nil, let id = message["id"] as? String,
                  let method = message["method"] as? String, Self.methods.contains(method),
                  let args = message["arguments"] as? [String: Any] else {
                throw LocalCUError.operation("Invalid or concurrent JavaScript native call")
            }
            let token = generation
            execution.calls += 1
            execution.nativeTask = Task { [weak self, weak execution] in
                guard let self, let execution else { return }
                var reply: [String: Any] = ["type": "reply", "exec_id": execution.id, "id": id]
                do {
                    let result = try await execution.invoke(method, args)
                    let images = (result.png.map { [$0] } ?? []) + result.images
                    let encodedBytes = images.reduce(0) { $0 + (($1.count + 2) / 3) * 4 }
                    guard execution.imageBytes + encodedBytes <= Self.maximumBytes else {
                        throw LocalCUError.operation("JavaScript screenshots exceed the 8 MiB output budget")
                    }
                    execution.images.append(contentsOf: images)
                    execution.imageBytes += encodedBytes
                    reply["ok"] = true; reply["value"] = result.data
                } catch { reply["ok"] = false; reply["error"] = error.localizedDescription }
                guard self.generation == token, self.execution === execution else { return }
                execution.nativeTask = nil
                self.send(reply)
            }
        case "result":
            guard execution.nativeTask == nil else { throw LocalCUError.operation("JavaScript completed with a native operation pending") }
            let data: [String: Any] = [
                "status": "completed", "value": message["value"] ?? NSNull(),
                "value_type": message["value_type"] ?? "undefined", "logs": message["logs"] ?? [],
                "logs_truncated": message["logs_truncated"] ?? false, "native_calls": execution.calls,
                "image_count": execution.images.count
            ]
            guard try JSONSerialization.data(withJSONObject: data).count + execution.imageBytes <= Self.maximumBytes else {
                throw LocalCUError.operation("JavaScript result and screenshots exceed the 8 MiB output budget")
            }
            deadline?.cancel(); deadline = nil
            self.execution = nil
            execution.continuation.resume(returning: DesktopResult(data: data, images: execution.images))
        case "error":
            deadline?.cancel(); deadline = nil
            self.execution = nil
            let logs = (message["logs"] as? [String] ?? []).joined(separator: "\n")
            execution.continuation.resume(throwing: LocalCUError.operation(
                "JavaScript: \(message["error"] as? String ?? "execution failed")" + (logs.isEmpty ? "" : "\n\(logs)")))
        default: throw LocalCUError.operation("Unknown JavaScript worker response")
        }
    }

    private func fail(_ message: String) {
        generation = UUID()
        deadline?.cancel(); deadline = nil
        let previous = execution
        execution = nil
        previous?.nativeTask?.cancel()
        if let process, process.isRunning { kill(process.processIdentifier, SIGKILL) }
        process = nil
        let previousInput = input
        input = nil
        writer.async { try? previousInput?.close() }
        buffer.removeAll(keepingCapacity: false)
        previous?.continuation.resume(throwing: LocalCUError.operation(message))
    }
}
