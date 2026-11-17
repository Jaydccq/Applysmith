import AppKit

extension DesktopController {
    /// Keeps the trees of the 8 windows observed most recently for diffs.
    func rememberTree(_ tree: [[String: Any]], key: String) {
        if previousTrees[key] == nil, previousTrees.count >= 8, let oldest = previousTreeOrder.first {
            previousTreeOrder.removeFirst()
            previousTrees.removeValue(forKey: oldest)
        }
        previousTreeOrder.removeAll { $0 == key }
        previousTreeOrder.append(key)
        previousTrees[key] = tree
    }

    func statePage(_ snapshot: Snapshot, args: [String: Any], includeImage: Bool, png: Data? = nil) throws -> DesktopResult {
        let offset = args["offset"] == nil ? 0 : try integer(args, "offset")
        let limit = args["limit"] == nil ? 4000 : try integer(args, "limit")
        guard offset >= 0, limit > 0, limit <= 4000 else { throw LocalCUError.invalidArgument("offset must be nonnegative and limit must be 1...4000") }
        var entries = snapshot.treeData
        var diff = false
        var removed: [Int] = []
        let key = "\(snapshot.pid):\(snapshot.windowID):\(snapshot.axScope)"
        if snapshot.axScope != "none", snapshot.diffEnabled, let previous = snapshot.previousTree {
                diff = true
                let old = Dictionary(uniqueKeysWithValues: previous.compactMap { node -> (Int, [String: Any])? in
                    guard let index = node["index"] as? Int else { return nil }; return (index, node)
                })
                entries = entries.filter { node in
                    guard let index = node["index"] as? Int, let before = old[index] else { return true }
                    return !NSDictionary(dictionary: before).isEqual(to: node)
                }
                let present = Set(snapshot.treeData.compactMap { $0["index"] as? Int })
                if !snapshot.treeTruncated { removed = old.keys.filter { !present.contains($0) }.sorted() }
        }
        if includeImage, snapshot.axScope != "none", !(snapshot.treeData.isEmpty && snapshot.axError != nil) {
            rememberTree(snapshot.treeData, key: key)
        }
        if let filter = args["ax_filter"] as? String, !filter.isEmpty {
            entries = entries.filter { node in
                ["role", "title", "description", "value", "identifier"].compactMap { node[$0] }.map { String(describing: $0) }
                    .joined(separator: " ").localizedCaseInsensitiveContains(filter)
            }
        }
        let page = Array(entries.dropFirst(offset).prefix(limit))
        var window: [String: Any] = ["title": snapshot.title, "rect": rectangleData(snapshot.windowRect)]
        if snapshot.hasAXWindow { window["ax_index"] = snapshot.windowIndex }
        window["window_id"] = Int(snapshot.windowID)
        var data: [String: Any] = ["snapshot_id": snapshot.id, "pid": Int(snapshot.pid),
                                  "launch_date": ISO8601DateFormatter().string(from: snapshot.launchDate),
                                  "expires_in_seconds": max(0, Int(lifetime - (ProcessInfo.processInfo.systemUptime - snapshot.created))),
                                  "window": window, "elements": page, "tree_truncated": snapshot.treeTruncated,
                                  "ax_scope": snapshot.axScope, "has_ax_window": snapshot.hasAXWindow,
                                  "offset": offset, "limit": limit, "total": entries.count, "has_more": offset + page.count < entries.count,
                                  "diff": diff, "removed_indices": removed,
                                  "frontmost": NSWorkspace.shared.frontmostApplication?.processIdentifier == snapshot.pid,
                                  "limits": ["nodes": 4000, "depth": 64, "seconds": 5, "text_bytes": 2 * 1024 * 1024]]
        if let error = snapshot.axError { data["ax_error"] = error }
        data["window_id"] = Int(snapshot.windowID)
        if let geometry = snapshot.imageGeometry, let screenshotID = snapshot.screenshotID {
            data["screenshot_id"] = screenshotID
            data["screenshot"] = ["screenshot_id": screenshotID, "pixel_width": geometry.pixelWidth, "pixel_height": geometry.pixelHeight,
                                  "coordinate_space": "screenshot_pixels", "origin": "top_left", "window_rect_points": rectangleData(geometry.windowRect)]
        }
        return DesktopResult(data: data, png: png)
    }
}
