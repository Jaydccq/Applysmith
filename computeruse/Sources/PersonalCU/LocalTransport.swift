import Foundation
import Darwin
import Logging
import MCP

/// Newline-delimited local MCP with bounded frames and one writer per connection.
/// The caller retains its descriptors; this transport owns only duplicated copies.
actor LocalTransport: Transport {
    nonisolated let logger = Logger(label: "personal-cu.transport", factory: { _ in SwiftLogNoOpLogHandler() })
    private static let inputLimit = 8 * 1024 * 1024
    private static let outputLimit = 16 * 1024 * 1024
    private static let queuedLimit = 32 * 1024 * 1024
    private struct Pending {
        let id: UUID
        let frame: Data
        let continuation: CheckedContinuation<Void, Error>
    }
    private let sourceInput: Int32
    private let sourceOutput: Int32
    private let stream: AsyncThrowingStream<Data, Error>
    private let continuation: AsyncThrowingStream<Data, Error>.Continuation
    private var input: Int32 = -1
    private var output: Int32 = -1
    private var connected = false
    private var closed = false
    private var reader: Task<Void, Never>?
    private var writer: Task<Void, Never>?
    private var readWait: DispatchSourceRead?
    private var queued: [Pending] = []
    private var active: Pending?
    private var queuedBytes = 0

    init(input: Int32 = STDIN_FILENO, output: Int32 = STDOUT_FILENO) {
        sourceInput = input
        sourceOutput = output
        let pair = AsyncThrowingStream<Data, Error>.makeStream(bufferingPolicy: .bufferingOldest(4))
        stream = pair.stream
        continuation = pair.continuation
    }

    func connect() async throws {
        guard !closed else { throw failure("Transport is closed") }
        if connected { return }
        let readFD = Darwin.dup(sourceInput)
        guard readFD >= 0 else { throw failure("Cannot duplicate input descriptor") }
        let writeFD = Darwin.dup(sourceOutput)
        guard writeFD >= 0 else { Darwin.close(readFD); throw failure("Cannot duplicate output descriptor") }
        do {
            for fd in [readFD, writeFD] {
                let flags = fcntl(fd, F_GETFL)
                guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) >= 0,
                      fcntl(fd, F_SETFD, FD_CLOEXEC) >= 0 else { throw failure("Cannot configure transport descriptors") }
            }
            guard fcntl(writeFD, F_SETNOSIGPIPE, 1) >= 0 else { throw failure("Cannot disable transport SIGPIPE") }
        } catch {
            Darwin.close(readFD); Darwin.close(writeFD)
            throw error
        }
        input = readFD
        output = writeFD
        connected = true
        reader = Task { await readFrames() }
    }

    func receive() -> AsyncThrowingStream<Data, Error> { stream }

    func disconnect() async { finish() }

    func send(_ data: Data) async throws {
        try Task.checkCancellation()
        guard connected else { throw failure("Transport is disconnected") }
        guard data.count <= Self.outputLimit else {
            let error = failure("MCP response exceeds 16 MiB")
            finish(error)
            throw error
        }
        guard queued.count < 32, queuedBytes + data.count + 1 <= Self.queuedLimit else {
            let error = failure("MCP output queue is full")
            finish(error)
            throw error
        }
        var frame = data
        frame.append(10)
        let id = UUID()
        try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { completion in
                queued.append(Pending(id: id, frame: frame, continuation: completion))
                queuedBytes += frame.count
                if writer == nil { writer = Task { await writeFrames() } }
            }
        }, onCancel: { Task { await self.cancelSend(id) } })
    }

    private func readFrames() async {
        var pending = Data()
        var bytes = [UInt8](repeating: 0, count: 65_536)
        do {
            while connected, !Task.isCancelled {
                let count = bytes.withUnsafeMutableBytes { Darwin.read(input, $0.baseAddress, $0.count) }
                if count == 0 { finish(); return }
                if count < 0 {
                    if errno == EINTR { continue }
                    if errno == EAGAIN || errno == EWOULDBLOCK {
                        try await waitReadable(); continue
                    }
                    throw failure("Cannot read MCP connection")
                }
                pending.append(contentsOf: bytes.prefix(count))
                while let newline = pending.firstIndex(of: 10) {
                    let size = pending.distance(from: pending.startIndex, to: newline)
                    guard size <= Self.inputLimit else { throw failure("MCP request exceeds 8 MiB") }
                    let message = Data(pending.prefix(size))
                    pending.removeSubrange(pending.startIndex...newline)
                    if message.isEmpty { continue }
                    switch continuation.yield(message) {
                    case .enqueued: break
                    case .dropped: throw failure("MCP input queue is full")
                    case .terminated: finish(); return
                    @unknown default: throw failure("MCP input stream failed")
                    }
                    // Allow the consumer to drain the bounded stream between frames.
                    await Task.yield()
                    guard connected else { return }
                }
                guard pending.count <= Self.inputLimit else { throw failure("MCP request exceeds 8 MiB") }
            }
        } catch { finish(error) }
    }

    /// Suspends until the input has data or end of file, or the transport
    /// finishes, so an idle connection costs no wakeups. The source watches a
    /// duplicate it closes itself, so finish may close the input at once.
    private func waitReadable() async throws {
        guard connected else { return }
        let fd = fcntl(input, F_DUPFD_CLOEXEC, 0)
        guard fd >= 0 else { throw failure("Cannot duplicate input descriptor") }
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            let source = DispatchSource.makeReadSource(fileDescriptor: fd, queue: .global())
            source.setEventHandler { source.cancel() }
            source.setCancelHandler { Darwin.close(fd); done.resume() }
            readWait = source
            source.resume()
        }
        readWait = nil
    }

    private func writeFrames() async {
        do {
            while connected, !queued.isEmpty {
                let item = queued.removeFirst()
                active = item
                var offset = 0
                var progress = ContinuousClock.now
                while offset < item.frame.count {
                    try Task.checkCancellation()
                    guard connected else { return }
                    let count = item.frame.withUnsafeBytes {
                        Darwin.write(output, $0.baseAddress!.advanced(by: offset), $0.count - offset)
                    }
                    if count > 0 { offset += count; progress = .now; continue }
                    if count < 0, errno == EINTR { continue }
                    if count < 0, errno == EAGAIN || errno == EWOULDBLOCK {
                        guard progress.duration(to: .now) < .seconds(30) else { throw failure("MCP client stopped reading") }
                        try await Task.sleep(for: .milliseconds(10)); continue
                    }
                    throw failure("Cannot write MCP connection")
                }
                active = nil
                queuedBytes -= item.frame.count
                item.continuation.resume()
            }
            writer = nil
        } catch { finish(error) }
    }

    private func cancelSend(_ id: UUID) {
        if active?.id == id {
            // A partial frame cannot be withdrawn without corrupting the stream.
            finish(CancellationError())
        } else if let index = queued.firstIndex(where: { $0.id == id }) {
            let item = queued.remove(at: index)
            queuedBytes -= item.frame.count
            item.continuation.resume(throwing: CancellationError())
        }
    }

    private func finish(_ error: Error? = nil) {
        guard !closed else { return }
        closed = true
        connected = false
        reader?.cancel(); reader = nil
        writer?.cancel(); writer = nil
        readWait?.cancel(); readWait = nil
        // Actor isolation prevents descriptor reuse during any read/write syscall.
        if input >= 0 { Darwin.close(input); input = -1 }
        if output >= 0 { Darwin.close(output); output = -1 }
        let sendError = error ?? failure("Transport is disconnected")
        active?.continuation.resume(throwing: sendError)
        active = nil
        for item in queued { item.continuation.resume(throwing: sendError) }
        queued.removeAll()
        queuedBytes = 0
        if let error { continuation.finish(throwing: error) } else { continuation.finish() }
    }

    private func failure(_ message: String) -> MCPError { .internalError(message) }
}
