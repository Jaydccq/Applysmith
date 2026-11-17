import Foundation
import AppKit
import MCP
import PersonalCUCore

/// One queue for all desktop sessions. Scripts never hold the gate while awaiting callbacks.
@MainActor
private final class DesktopGate {
    static let shared = DesktopGate()
    private var occupied = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    func acquire() async {
        if !occupied { occupied = true; return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func release() {
        if waiters.isEmpty { occupied = false } else { waiters.removeFirst().resume() }
    }
}

@MainActor
final class MCPHost {
    private let controller = DesktopController()
    private let javascript = JavaScriptBridge()
    private var closed = false
    private var server: Server?
    private var calls: [UUID: Task<DesktopResult, Error>] = [:]
    private var inputCalls = Set<UUID>()
    private let identity = UUID()
    private static let mutations = Set(["click", "type_text", "paste", "press_key", "scroll", "set_value", "perform_secondary_action", "select_text", "drag", "drag_paths", "activate_app", "activate_window", "launch_app"])
    private static let inputs = Set(["click", "type_text", "paste", "press_key", "scroll", "set_value", "perform_secondary_action", "select_text", "drag", "drag_paths"])

    init() {
        ServiceState.shared.inputCancellations[identity] = { [weak self] in
            guard let self else { return }
            for id in self.inputCalls { self.calls[id]?.cancel() }
            self.javascript.reset()
        }
    }

    func run(transport: any Transport) async throws {
        let server = Server(name: "personal-cu", version: "0.2.0", capabilities: .init(tools: .init()), configuration: .strict)
        self.server = server
        await server.withMethodHandler(ListTools.self) { _ in .init(tools: ToolCatalog.definitions.map(\.tool)) }
        await server.withMethodHandler(CallTool.self) { [self] params in
            do {
                let encoded = try JSONEncoder().encode(params.arguments ?? [:])
                let arguments = try JSONSerialization.jsonObject(with: encoded) as? [String: Any] ?? [:]
                let result = try await dispatch(params.name, arguments: arguments)
                let text = String(decoding: try JSONSerialization.data(withJSONObject: result.data, options: [.sortedKeys]), as: UTF8.self)
                var content: [Tool.Content] = [.text(text)]
                let images = result.png.map { [$0] } ?? []
                for png in images + result.images { content.append(.image(data: png.base64EncodedString(), mimeType: "image/png", metadata: nil)) }
                return .init(content: content, isError: false)
            } catch { return .init(content: [.text(error.localizedDescription)], isError: true) }
        }
        do {
            try Task.checkCancellation()
            guard !closed else { throw CancellationError() }
            try await server.start(transport: transport)
            await withTaskCancellationHandler(operation: {
                if Task.isCancelled { await server.stop() }
                await server.waitUntilCompleted()
            }, onCancel: {
                Task { @MainActor in await self.shutdown() }
            })
        } catch {
            await shutdown()
            throw error
        }
        await shutdown()
    }

    func beginShutdown() {
        guard !closed else { return }
        closed = true
        ServiceState.shared.inputCancellations.removeValue(forKey: identity)
        for task in calls.values { task.cancel() }
        javascript.reset()
    }

    func shutdown() async {
        beginShutdown()
        // Disconnect first: the server receive loop must not keep shutdown waiting.
        await server?.stop()
        let pending = Array(calls.values)
        for task in pending { _ = await task.result }
        // JS reset cancels its native callback separately from the outer js call.
        // Every input holds this gate until its cancellation release defers finish;
        // closing admission before the barrier prevents a later callback from posting.
        await DesktopGate.shared.acquire()
        DesktopGate.shared.release()
        server = nil
    }

    private func dispatch(_ name: String, arguments: [String: Any]) async throws -> DesktopResult {
        guard !closed else { throw CancellationError() }
        let id = UUID()
        let task = Task { @MainActor in try await self.invoke(name, arguments: arguments) }
        calls[id] = task
        if Self.mutations.contains(name) || name == "js" { inputCalls.insert(id) }
        defer { calls.removeValue(forKey: id); inputCalls.remove(id) }
        return try await withTaskCancellationHandler(operation: { try await task.value }, onCancel: { task.cancel() })
    }

    /// The name of the app an action targets, for the manager's list of recent actions.
    private static func appName(_ arguments: [String: Any]) -> String? {
        let pid = (try? WindowService.resolvePID(arguments, optional: true)) ?? (arguments["window_id"] != nil ? (try? WindowService.window(arguments))?.pid : nil)
        return pid.flatMap { NSRunningApplication(processIdentifier: $0)?.localizedName }
    }

    func invoke(_ name: String, arguments: [String: Any]) async throws -> DesktopResult {
        guard !closed else { throw CancellationError() }
        try Task.checkCancellation()
        guard let definition = ToolCatalog.definitions.first(where: { $0.name == name }) else { throw ArgumentError("Unknown tool: \(name)") }
        try definition.validate(arguments)
        if name == "js" {
            return try await javascript.execute(code: arguments["code"] as! String, timeoutMS: arguments["timeout_ms"] as? Int ?? 30_000) { [self] name, args in
                try await invoke(name, arguments: args)
            }
        }
        if name == "js_reset" { javascript.reset(); return DesktopResult(data: ["status": "reset"]) }
        if name == "debug_tap" {
            guard let pid = try WindowService.resolvePID(arguments) else { throw ArgumentError("Missing target") }
            return try await DebugTap.capture(pid: pid, seconds: arguments["seconds"] as? Double ?? 1)
        }
        let state = ServiceState.shared
        // The doctor reads permissions and service state only, so it answers
        // at once instead of waiting behind an action in the desktop queue.
        if name == "doctor" {
            var result = try await controller.call(name, arguments: arguments)
            result.data["service"] = ["running": state.socketReady, "paused": state.paused, "background_enabled": state.backgroundEnabled, "clients": state.clients, "socket": RuntimeEndpoint.path]
            result.data["tool_count"] = ToolCatalog.definitions.count
            result.data["input_monitoring"] = CGPreflightListenEventAccess()
            result.data["desktop_session"] = DesktopSession.status
            if let front = NSWorkspace.shared.frontmostApplication {
                result.data["frontmost_pid"] = Int(front.processIdentifier)
            }
            if let point = CGEvent(source: nil)?.location {
                result.data["pointer"] = ["x": point.x, "y": point.y]
            }
            return result
        }
        await DesktopGate.shared.acquire()
        defer { DesktopGate.shared.release() }
        try Task.checkCancellation()
        guard !closed else { throw CancellationError() }
        if Self.mutations.contains(name), state.paused { throw ArgumentError("Control is paused in PersonalCU. Read-only tools remain available.") }
        if Self.inputs.contains(name), !state.backgroundEnabled, arguments["input_mode"] as? String != "foreground" {
            throw ArgumentError("Background control is disabled. Enable it in PersonalCU, or explicitly request foreground input.")
        }
        if Self.mutations.contains(name) { state.record(name, app: Self.appName(arguments)) }
        switch name {
        case "list_apps", "list_windows", "get_window", "launch_app", "activate_window":
            return try await WindowService.call(name, arguments: arguments)
        case "get_app_state", "get_window_state":
            var args = arguments
            if name == "get_window_state" {
                guard args["window_id"] != nil else { throw ArgumentError("get_window_state requires window_id") }
                let image = args.removeValue(forKey: "include_screenshot") as? Bool ?? true
                let text = args.removeValue(forKey: "include_text") as? Bool ?? true
                guard image || text else { throw ArgumentError("Select text or screenshot") }
                args["mode"] = image ? (text ? "full" : "image") : "ax"
            }
            args = try WindowService.stateArguments(args)
            if args["mode"] == nil { args["mode"] = "full" }
            var result = try await controller.call("get_app_state", arguments: args)
            if let pid = result.data["pid"] as? Int {
                result.data["windows"] = WindowService.windows(pid: pid_t(pid)).map(\.json)
                result.data.merge(WindowService.inputTraits(pid: pid_t(pid))) { _, new in new }
            }
            return result
        case "activate_app":
            var args = arguments
            if args["window_id"] != nil {
                args["pid"] = Int(try WindowService.window(args).pid)
                args.removeValue(forKey: "bundle_id")
                args.removeValue(forKey: "window_id")
            }
            return try await controller.call(name, arguments: args)
        default: return try await controller.call(name, arguments: arguments)
        }
    }
}
