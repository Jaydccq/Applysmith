import Foundation
import CoreFoundation
import MCP

struct ArgumentError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

struct ToolDefinition {
    let name: String
    let description: String
    let properties: [String: Value]
    var required: [String] = []
    var schema: Value { .object(["type": "object", "properties": .object(properties), "required": .array(required.map(Value.string)), "additionalProperties": false]) }
    var tool: Tool { Tool(name: name, description: description, inputSchema: schema) }
    func validate(_ arguments: [String: Any]) throws {
        try InputSchema.validate(arguments, schema: schema, path: "arguments")
        if name == "get_app_state" || name == "activate_app" {
            guard arguments["window_id"] != nil || (arguments["pid"] != nil) != (arguments["bundle_id"] != nil) else { throw ArgumentError("Supply pid OR bundle_id, or window_id") }
        }
        if name == "click" {
            let index = arguments["index"] != nil
            let point = arguments["x"] != nil && arguments["y"] != nil
            guard index != point, !index || (arguments["x"] == nil && arguments["y"] == nil) else { throw ArgumentError("Supply index OR x and y") }
        }
    }
}

/// Enforces only the JSON Schema vocabulary used by our repository-owned catalog.
/// Unsupported schema vocabulary is never accepted from clients.
enum InputSchema {
    static func bound(_ v: Value?) -> Double? { v?.intValue.map(Double.init) ?? v?.doubleValue }
    static func validate(_ value: Any, schema: Value, path: String, depth: Int = 0) throws {
        guard depth <= 20, case .object(let s) = schema else { throw ArgumentError("Invalid nested input at \(path)") }
        func fail(_ message: String) throws -> Never { throw ArgumentError("\(path): \(message)") }
        let n = value as? NSNumber
        let number = n.map { CFGetTypeID($0) != CFBooleanGetTypeID() && $0.doubleValue.isFinite } ?? false
        switch s["type"]?.stringValue {
        case "object":
            guard let object = value as? [String: Any], case .object(let properties) = s["properties"] else { try fail("must be an object") }
            if case .array(let required) = s["required"] {
                for name in required.compactMap(\.stringValue) where object[name] == nil { try fail("missing \(name)") }
            }
            let unknown = Set(object.keys).subtracting(properties.keys)
            if s["additionalProperties"] == .bool(false), !unknown.isEmpty { try fail("unknown fields \(unknown.sorted())") }
            for (key, item) in object { if let child = properties[key] { try validate(item, schema: child, path: path + "." + key, depth: depth + 1) } }
        case "array":
            guard let array = value as? [Any], let itemSchema = s["items"] else { try fail("must be an array") }
            if let min = s["minItems"]?.intValue, array.count < min { try fail("too few items") }
            if let max = s["maxItems"]?.intValue, array.count > max { try fail("too many items") }
            for (i, item) in array.enumerated() { try validate(item, schema: itemSchema, path: "\(path)[\(i)]", depth: depth + 1) }
        case "integer", "number":
            guard number, let n else { try fail("must be finite numeric input, not boolean") }
            if s["type"] == "integer", n.doubleValue.rounded() != n.doubleValue { try fail("must be an integer") }
            guard abs(n.doubleValue) <= 9_007_199_254_740_991 else { try fail("outside exact JSON integer range") }
            if let min = bound(s["minimum"]), n.doubleValue < min { try fail("below minimum") }
            if let max = bound(s["maximum"]), n.doubleValue > max { try fail("above maximum") }
        case "boolean": guard let n, CFGetTypeID(n) == CFBooleanGetTypeID() else { try fail("must be boolean") }
        case "string":
            guard let text = value as? String else { try fail("must be a string") }
            if let max = s["maxLength"]?.intValue, text.unicodeScalars.count > max { try fail("text is too long") }
            if let min = s["minLength"]?.intValue, text.unicodeScalars.count < min { try fail("text is empty") }
            if case .array(let allowed) = s["enum"], !allowed.contains(.string(text)) { try fail("unsupported value") }
        default: try fail("unsupported input schema")
        }
    }
}

enum ToolCatalog {
    static func string(_ choices: [String]? = nil, max: Int = 65_536) -> Value {
        var value: [String: Value] = ["type": "string", "maxLength": .int(max)]
        if let choices { value["enum"] = .array(choices.map(Value.string)) }
        return .object(value)
    }
    static func integer(_ min: Int = 0, _ max: Int = Int(Int32.max)) -> Value { .object(["type": "integer", "minimum": .int(min), "maximum": .int(max)]) }
    static func number(_ min: Double = -1e9, _ max: Double = 1e9) -> Value { .object(["type": "number", "minimum": .double(min), "maximum": .double(max)]) }
    static func boolean() -> Value { .object(["type": "boolean"]) }
    static func array(_ item: Value, min: Int = 0, max: Int) -> Value { .object(["type": "array", "items": item, "minItems": .int(min), "maxItems": .int(max)]) }
    static func object(_ props: [String: Value], required: [String]) -> Value { .object(["type": "object", "properties": .object(props), "required": .array(required.map(Value.string)), "additionalProperties": false]) }
    static func merge(_ lhs: [String: Value], _ rhs: [String: Value]) -> [String: Value] { lhs.merging(rhs) { _, r in r } }
    static let target: [String: Value] = ["pid": integer(1), "bundle_id": string(max: 512), "window_id": integer(1, Int(UInt32.max))]
    static let action: [String: Value] = ["pid": integer(1), "snapshot_id": string(max: 128), "screenshot_id": string(max: 128), "input_mode": string(["background", "foreground"])]
    static let point: [String: Value] = ["index": integer(), "x": number(0), "y": number(0)]
    static let rect = object(["x": number(0), "y": number(0), "width": number(0.01), "height": number(0.01)], required: ["x", "y", "width", "height"])
    static let observation: [String: Value] = merge(target, ["mode": string(["ax", "full", "image"]), "rect": rect, "ax_filter": string(max: 4096), "offset": integer(0, 4000), "limit": integer(1, 1000), "snapshot_id": string(max: 128), "disable_diff": boolean(), "additional_window_ids": array(integer(1, Int(UInt32.max)), max: 3)])
    static let stroke = object(["points": array(array(number(0), min: 2, max: 2), min: 2, max: 1024), "steps": integer(1, 500), "hold_ms": integer(0, 1000), "step_ms": integer(0, 100)], required: ["points"])
    static let definitions: [ToolDefinition] = [
        .init(name: "doctor", description: "Check permissions, service state and input backends without prompting.", properties: [:]),
        .init(name: "list_apps", description: "Discover installed and running local applications.", properties: ["running_only": boolean()]),
        .init(name: "list_windows", description: "List native window IDs and owning applications; optionally filter target.", properties: ["pid": integer(1), "bundle_id": string(max: 512)]),
        .init(name: "get_window", description: "Resolve an exact native window and verify ownership.", properties: target, required: ["window_id"]),
        .init(name: "launch_app", description: "Launch an installed app by bundle identifier or absolute .app path.", properties: ["app": string(max: 4096)], required: ["app"]),
        .init(name: "activate_app", description: "Explicitly activate a selected app.", properties: target),
        .init(name: "activate_window", description: "Explicitly activate, unminimize and raise an exact window.", properties: target, required: ["window_id"]),
        .init(name: "get_app_state", description: "Read AX state and optional screenshots; pagination uses one saved snapshot; coordinates are screenshot pixels.", properties: observation),
        .init(name: "get_window_state", description: "Read an exact window with optional text and screenshot.", properties: merge(observation, ["include_screenshot": boolean(), "include_text": boolean()]), required: ["window_id"]),
        .init(name: "click", description: "Target-bound indexed or screenshot-coordinate click. Never repeats an uncertain delivered action.", properties: merge(merge(action, point), ["button": string(["left", "right", "middle"]), "count": integer(1, 3), "hold_ms": integer(0, 500), "hover_ms": integer(0, 1000), "verify_after": boolean()]), required: ["pid", "snapshot_id"]),
        .init(name: "type_text", description: "Unicode input with optional target, clear and submit; verify before submit where observable.", properties: merge(merge(action, point), ["text": string(max: 4096), "clear": boolean(), "submit": boolean()]), required: ["pid", "snapshot_id", "text"]),
        .init(name: "paste", description: "Paste text/Markdown/HTML and restore the clipboard if it is still owned by this operation.", properties: merge(action, ["text": string(max: 1_048_576), "format": string(["text", "md", "html"])]), required: ["pid", "snapshot_id", "text"]),
        .init(name: "press_key", description: "Targeted key or chord DSL, e.g. command+a, Return, shift+tab.", properties: merge(action, ["keys": string(max: 256)]), required: ["pid", "snapshot_id", "keys"]),
        .init(name: "scroll", description: "Targeted scrolling at an AX index or screenshot point. Positive vertical pixels/pages scroll up; negative scrolls down.", properties: merge(merge(action, point), ["delta_y": integer(-1000, 1000), "delta_x": integer(-1000, 1000), "page": integer(-20, 20)]), required: ["pid", "snapshot_id"]),
        .init(name: "set_value", description: "Write a writable AX value and verify readback; optional search submit.", properties: merge(action, ["index": integer(), "value": string(), "autosubmit": boolean()]), required: ["pid", "snapshot_id", "index", "value"]),
        .init(name: "perform_secondary_action", description: "Perform an advertised AX action on a saved element, default AXShowMenu.", properties: merge(action, ["index": integer(), "action": string(max: 128)]), required: ["pid", "snapshot_id", "index"]),
        .init(name: "select_text", description: "Select UTF-16 range or exact text with optional prefix/suffix disambiguation.", properties: merge(action, ["index": integer(), "start": integer(), "length": integer(), "text": string(), "prefix": string(), "suffix": string(), "selection": string(["text", "cursor_before", "cursor_after"])]), required: ["pid", "snapshot_id", "index"]),
        .init(name: "drag", description: "Drag between screenshot pixel points with bounded timing and interpolation. In a Chromium or Electron window the left button is reported as pressed for the length of the drag, so the page sees event.buttons set (button_state in the result); when that could not be done, the result carries button_state_note and a page that tests event.buttons does not see the drag.", properties: merge(action, ["from_x": number(0), "from_y": number(0), "to_x": number(0), "to_y": number(0), "steps": integer(1, 500), "hold_ms": integer(0, 1000), "step_ms": integer(0, 100)]), required: ["pid", "snapshot_id", "from_x", "from_y", "to_x", "to_y"]),
        .init(name: "drag_paths", description: "Draw bounded multi-stroke screenshot paths, preserving every waypoint. What drag does in a Chromium window applies to each path.", properties: merge(action, ["paths": array(stroke, min: 1, max: 500), "abort_if_cursor_in_window": boolean()]), required: ["pid", "snapshot_id", "paths"]),
        .init(name: "debug_tap", description: "Observe bounded target input-event diagnostics without recording typed text.", properties: ["pid": integer(1), "seconds": number(0.1, 10)], required: ["pid"]),
        .init(name: "js", description: "Run persistent trusted JavaScript with top-level await and cu native tools. This is code execution, not a security sandbox.", properties: ["code": string(max: 1_048_576), "timeout_ms": integer(1, 600_000)], required: ["code"]),
        .init(name: "js_reset", description: "Terminate and reset this connection's JavaScript process and state.", properties: [:])
    ]
}
