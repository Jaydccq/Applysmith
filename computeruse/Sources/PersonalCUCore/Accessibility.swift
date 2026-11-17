import AppKit
import ApplicationServices

enum AXReader {
    @TaskLocal static var readDeadline: TimeInterval?
    struct BudgetExceeded: LocalizedError {
        var errorDescription: String? { "Accessibility inspection exceeded its bounded read deadline" }
    }

    static func boundedRead<T>(_ body: () throws -> T) rethrows -> T {
        let deadline = min(readDeadline ?? .infinity, ProcessInfo.processInfo.systemUptime + 5)
        return try $readDeadline.withValue(deadline, operation: body)
    }

    static func prepare(_ element: AXUIElement) throws {
        try Task.checkCancellation()
        let remaining = readDeadline.map { $0 - ProcessInfo.processInfo.systemUptime } ?? 5
        guard remaining > 0 else { throw BudgetExceeded() }
        // Apple documents that an AX timeout belongs to this exact object; children do not inherit it.
        let error = AXUIElementSetMessagingTimeout(element, Float(min(5, remaining)))
        guard error == .success else { throw LocalCUError.accessibility("Set AX timeout", error.rawValue) }
    }
    struct ElementDescriptor: Equatable {
        let role: String
        let subrole: String?
        let title: String?
        let description: String?
        let identifier: String?
        let value: NSObject?
        let rectangle: CGRect?
    }

    static func descriptor(_ element: AXUIElement) throws -> ElementDescriptor {
        let role = try string(element, kAXRoleAttribute) ?? "unknown"
        let subrole = try string(element, kAXSubroleAttribute)
        let rawValue = subrole == kAXSecureTextFieldSubrole ? nil : try value(element, kAXValueAttribute)
        let comparableValue: NSObject?
        if let text = rawValue as? String { comparableValue = text as NSString }
        else if let number = rawValue as? NSNumber { comparableValue = number }
        else { comparableValue = nil }
        return try ElementDescriptor(role: role, subrole: subrole,
                                     title: string(element, kAXTitleAttribute),
                                     description: string(element, kAXDescriptionAttribute),
                                     identifier: string(element, kAXIdentifierAttribute),
                                     value: comparableValue, rectangle: rectangle(element))
    }

    static func checkPermission() throws {
        guard AXIsProcessTrusted() else {
            throw LocalCUError.permission("Enable Accessibility for the application or executable hosting Personal CU in System Settings, then retry. No permission prompt was requested.")
        }
    }

    /// Display metadata can be absent even when a provider reports generic
    /// failure. Subrole stays strict: it classifies secure text before AXValue.
    static func mayOmitOptionalMetadata(_ attribute: String, error: AXError) -> Bool {
        [.failure, .illegalArgument].contains(error)
            && [kAXDescriptionAttribute, kAXIdentifierAttribute, kAXHelpAttribute, kAXTitleAttribute].contains(attribute)
    }

    static func value(_ element: AXUIElement, _ attribute: String) throws -> CFTypeRef? {
        try prepare(element)
        var result: CFTypeRef?
        let error = AXUIElementCopyAttributeValue(element, attribute as CFString, &result)
        if error != .success, let readDeadline, ProcessInfo.processInfo.systemUptime >= readDeadline { throw BudgetExceeded() }
        if mayOmitOptionalMetadata(attribute, error: error) { return nil }
        switch error {
        case .success: return result
        case .attributeUnsupported, .noValue: return nil
        default: throw LocalCUError.accessibility("Read \(attribute)", error.rawValue)
        }
    }

    static func string(_ element: AXUIElement, _ attribute: String) throws -> String? {
        try value(element, attribute) as? String
    }

    static func elements(_ element: AXUIElement, _ attribute: String) throws -> [AXUIElement] {
        guard let value = try value(element, attribute) else { return [] }
        guard let result = value as? [AXUIElement] else {
            throw LocalCUError.operation("Accessibility returned an invalid \(attribute) collection")
        }
        return result
    }

    static func rectangle(_ element: AXUIElement) throws -> CGRect? {
        guard let position = try value(element, kAXPositionAttribute),
              let size = try value(element, kAXSizeAttribute),
              CFGetTypeID(position) == AXValueGetTypeID(), CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
        var point = CGPoint.zero
        var extent = CGSize.zero
        guard AXValueGetValue(position as! AXValue, .cgPoint, &point),
              AXValueGetValue(size as! AXValue, .cgSize, &extent) else {
            throw LocalCUError.operation("Accessibility returned invalid window geometry")
        }
        guard point.x.isFinite, point.y.isFinite, extent.width.isFinite, extent.height.isFinite,
              extent.width >= 0, extent.height >= 0 else {
            throw LocalCUError.operation("Accessibility returned nonfinite or negative geometry")
        }
        return CGRect(origin: point, size: extent)
    }

    /// The status of a performed AX action. A control that opens a menu can
    /// hold the app's answer until the menu closes, so the call reports
    /// cannotComplete after its timeout although the action ran: that is an
    /// unverified delivery, never a failure a caller would repeat.
    static func actionStatus(_ error: AXError, _ operation: String) throws -> String {
        switch error {
        case .success: return "performed"
        case .cannotComplete: return "posted_unverified"
        default: throw LocalCUError.accessibility(operation, error.rawValue)
        }
    }

    static func actions(_ element: AXUIElement) throws -> [String] {
        try prepare(element)
        var values: CFArray?
        let error = AXUIElementCopyActionNames(element, &values)
        switch error {
        case .success: return values as? [String] ?? []
        case .actionUnsupported, .notImplemented: return []
        default: throw LocalCUError.accessibility("Read available actions", error.rawValue)
        }
    }

    static func writable(_ element: AXUIElement, _ attribute: String) throws -> Bool {
        try prepare(element)
        var result = DarwinBoolean(false)
        let error = AXUIElementIsAttributeSettable(element, attribute as CFString, &result)
        switch error {
        case .success: return result.boolValue
        case .attributeUnsupported: return false
        default: throw LocalCUError.accessibility("Inspect writable \(attribute)", error.rawValue)
        }
    }

    struct Tree {
        var data: [[String: Any]] = []
        var elements: [AXUIElement] = []
        var descriptors: [ElementDescriptor] = []
        var truncated = false
    }

    static func tree(_ root: AXUIElement) throws -> Tree {
        var result = Tree()
        var visited = Set<CFHashCode>()
        let deadline = ProcessInfo.processInfo.systemUptime + 5
        var textBytes = 0
        func walk(_ element: AXUIElement, parent: Int?, depth: Int) throws {
            guard result.elements.count < 4000, ProcessInfo.processInfo.systemUptime < deadline else { result.truncated = true; return }
            // AX hierarchies occasionally contain cycles. A hash collision can only omit a node.
            guard visited.insert(CFHash(element)).inserted else { return }
            let index = result.elements.count
            let descriptor = try descriptor(element)
            var node: [String: Any] = ["index": index, "depth": depth]
            if let parent { node["parent"] = parent }
            node["role"] = descriptor.role
            if let subrole = descriptor.subrole { node["subrole"] = subrole }
            for (key, text) in [("title", descriptor.title), ("description", descriptor.description), ("identifier", descriptor.identifier)] {
                if let text { node[key] = String(text.prefix(4096)) }
            }
            let secure = descriptor.subrole == kAXSecureTextFieldSubrole
            node["secure"] = secure
            if let value = descriptor.value {
                if let text = value as? String { node["value"] = String(text.prefix(4096)) }
                else if let number = value as? NSNumber { node["value"] = number }
            }
            if let rect = descriptor.rectangle { node["rect"] = rectangleData(rect) }
            if let enabled = try value(element, kAXEnabledAttribute) as? Bool { node["enabled"] = enabled }
            node["actions"] = try actions(element)
            node["value_writable"] = try (!secure && writable(element, kAXValueAttribute))
            let rawText = [descriptor.role, descriptor.subrole, descriptor.title, descriptor.description, descriptor.identifier, descriptor.value as? String]
                .compactMap { $0 }.reduce(0) { $0 + $1.utf8.count }
            let bytes = max(rawText, try JSONSerialization.data(withJSONObject: node).count)
            guard textBytes + bytes <= 2 * 1024 * 1024 else { result.truncated = true; return }
            textBytes += bytes
            result.elements.append(element)
            result.descriptors.append(descriptor)
            result.data.append(node)
            let children = try elements(element, kAXChildrenAttribute)
            guard depth < 64 else { if !children.isEmpty { result.truncated = true }; return }
            for child in children {
                guard result.elements.count < 4000 else { result.truncated = true; break }
                try walk(child, parent: index, depth: depth + 1)
            }
        }
        do {
            try $readDeadline.withValue(deadline) { try walk(root, parent: nil, depth: 0) }
        } catch is BudgetExceeded { result.truncated = true }
        catch {
            guard ProcessInfo.processInfo.systemUptime >= deadline else { throw error }
            result.truncated = true
        }
        return result
    }
}
