import ApplicationServices
import CoreGraphics
import Darwin
import Foundation
import Testing
@testable import PersonalCUCore

struct AccessibilityMetadataTests {
    @Test func absentDisplayMetadataDoesNotAbortTreeInspection() {
        // The authorized resident service reproduced AXDescription / -25200.
        for attribute in [kAXDescriptionAttribute, kAXIdentifierAttribute, kAXHelpAttribute, kAXTitleAttribute] {
            #expect(AXReader.mayOmitOptionalMetadata(attribute, error: .failure))
            #expect(AXReader.mayOmitOptionalMetadata(attribute, error: .illegalArgument))
        }
    }

    @Test func securityClassificationAndRequiredAttributesRemainStrict() {
        for attribute in [kAXSubroleAttribute, kAXRoleAttribute, kAXValueAttribute, kAXPositionAttribute, kAXSizeAttribute, kAXChildrenAttribute] {
            #expect(!AXReader.mayOmitOptionalMetadata(attribute, error: .failure))
            #expect(!AXReader.mayOmitOptionalMetadata(attribute, error: .illegalArgument))
        }
    }

    @Test func aTimedOutActionIsUnverifiedNotFailed() throws {
        // AXPress or AXShowMenu on a control that opens a menu answers cannotComplete once the menu holds the app.
        #expect(try AXReader.actionStatus(.success, "AXPress") == "performed")
        #expect(try AXReader.actionStatus(.cannotComplete, "AXPress") == "posted_unverified")
        for error: AXError in [.failure, .invalidUIElement, .actionUnsupported, .apiDisabled] {
            #expect(throws: LocalCUError.self) { try AXReader.actionStatus(error, "AXPress") }
        }
    }

    @Test func optionalMetadataStillPropagatesTargetAndPermissionFailures() {
        for error: AXError in [.invalidUIElement, .cannotComplete, .apiDisabled, .notImplemented] {
            #expect(!AXReader.mayOmitOptionalMetadata(kAXDescriptionAttribute, error: error))
        }
        #expect(!AXReader.mayOmitOptionalMetadata(kAXDescriptionAttribute, error: .success))
    }
}

struct GeometryAndIdentityTests {
    @Test
    func testRetinaScreenshotPixelsMapToGlobalWindowPoints() throws {
        let geometry = ScreenshotGeometry(windowRect: CGRect(x: 300, y: 150, width: 600, height: 400), pixelWidth: 1200, pixelHeight: 800)
        #expect(try geometry.globalPoint(x: 600, y: 400) == CGPoint(x: 600, y: 350))
        #expect(try geometry.globalPoint(x: 0, y: 0) == CGPoint(x: 300, y: 150))
    }

    @Test
    func testSecondaryDisplayNegativeOriginAndActualEncodedSize() throws {
        let geometry = ScreenshotGeometry(windowRect: CGRect(x: -1200, y: -200, width: 1000, height: 500), pixelWidth: 1500, pixelHeight: 1000)
        #expect(try geometry.globalPoint(x: 750, y: 500) == CGPoint(x: -700, y: 50))
    }

    @Test
    func testRejectsOutsideAndNonfiniteCoordinates() throws {
        let geometry = ScreenshotGeometry(windowRect: CGRect(x: 0, y: 0, width: 500, height: 300), pixelWidth: 1000, pixelHeight: 600)
        for (x, y) in [(1000.0, 0.0), (0, 600), (-0.01, 0), (0, -1), (.infinity, 1), (.nan, 1)] {
            #expect(throws: LocalCUError.self) { try geometry.globalPoint(x: x, y: y) }
        }
        _ = try geometry.globalPoint(x: 999.9, y: 599.9)
    }

    @Test
    func testSnapshotBindsPIDAndLaunchIdentityAndExpiresAtThirtySeconds() throws {
        let launch = Date(timeIntervalSince1970: 12345)
        let identity = SnapshotIdentity(pid: 123, launchDate: launch, created: 100)
        try identity.validate(requestedPID: 123, currentLaunchDate: launch, terminated: false, now: 129.99)
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 124, currentLaunchDate: launch, terminated: false, now: 101) }
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 123, currentLaunchDate: launch.addingTimeInterval(1), terminated: false, now: 101) }
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 123, currentLaunchDate: launch, terminated: true, now: 101) }
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 123, currentLaunchDate: nil, terminated: false, now: 101) }
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 123, currentLaunchDate: launch, terminated: false, now: 130) }
        #expect(throws: LocalCUError.self) { try identity.validate(requestedPID: 123, currentLaunchDate: launch, terminated: false, now: 99) }
    }

    @Test
    func testGeometryChangesRequireAnotherSnapshot() {
        let rectangle = CGRect(x: 10, y: 20, width: 500, height: 400)
        #expect(sameRectangle(rectangle, rectangle))
        #expect(!sameRectangle(rectangle, rectangle.offsetBy(dx: 1, dy: 0)))
        #expect(!sameRectangle(rectangle, CGRect(x: 10, y: 20, width: 501, height: 400)))
        // A window that gained or lost the focus is one point larger or smaller on every side; it has not moved.
        let inactive = CGRect(x: 475, y: 254, width: 520, height: 590), key = CGRect(x: 474, y: 253, width: 522, height: 592)
        #expect(sameWindowFrame(inactive, key) && sameWindowFrame(key, inactive) && sameWindowFrame(key, key))
        // Seen between the two: one axis has the border and the other not yet.
        #expect(sameWindowFrame(key, CGRect(x: 475, y: 253, width: 520, height: 592)))
        for moved in [inactive.offsetBy(dx: 1, dy: 0), inactive.offsetBy(dx: -1, dy: -1), key.insetBy(dx: -1, dy: -1),
                      CGRect(x: 474, y: 253, width: 522, height: 590), CGRect(x: 475, y: 254, width: 522, height: 592)] {
            #expect(!sameWindowFrame(inactive, moved))
        }
    }

    @Test
    func testElementDescriptorDetectsRelabelledAndEditedControls() {
        let rect = CGRect(x: 10, y: 20, width: 300, height: 40)
        func descriptor(title: String = "Name", value: String = "before", rectangle: CGRect? = nil) -> AXReader.ElementDescriptor {
            AXReader.ElementDescriptor(role: "AXTextField", subrole: nil, title: title, description: nil,
                                       identifier: "fixture.name", value: value as NSString, rectangle: rectangle ?? rect)
        }
        #expect(descriptor() == descriptor())
        #expect(descriptor() != descriptor(title: "Delete"))
        #expect(descriptor() != descriptor(value: "user edited this"))
        #expect(descriptor() != descriptor(rectangle: rect.offsetBy(dx: 1, dy: 0)))
        // Values are compared in full even when the displayed tree truncates long content.
        let prefix = String(repeating: "x", count: 5000)
        #expect(descriptor(value: prefix + "A") != descriptor(value: prefix + "B"))
    }
}

struct KeyAndProcessTests {
    @Test func batchesAndShortcutModifiers() throws {
        let keys = try KeyDSL.parse("cmd+a backspace shift+tab Return")
        #expect(keys.count == 4)
        #expect(keys[0].code == 0 && keys[0].flags == .maskCommand)
        #expect(keys[1].code == 51)
        #expect(keys[2].code == 48 && keys[2].flags == .maskShift)
        #expect(keys[3].code == 36)
    }
    @Test func shiftedPunctuationAndCapitalLetter() throws {
        let keys = try KeyDSL.parse("command++ ? A")
        #expect(keys.count == 3)
        #expect(keys[0].code == 24 && keys[0].flags == [.maskCommand, .maskShift])
        #expect(keys[1].code == 44 && keys[1].flags == .maskShift)
        #expect(keys[2].code == 0 && keys[2].flags == .maskShift)
    }
    @Test func malformedKeysFailBeforeDelivery() {
        for input in ["", "command+", "unsupported+a", "not-a-key"] {
            #expect(throws: (any Error).self) { try KeyDSL.parse(input) }
        }
    }
    @Test func typedTextCarriesNoKeyTheCallerDidNotName() throws {
        // A control character in a Unicode key event is that key: "\r" is Return.
        for input in ["ok\r", "line\nline", "a\tb", "a\u{1b}", "a\u{7f}", "a\u{8}", "a\u{85}", "a\u{2028}b", "a\u{2029}b", "\r\n"] {
            #expect(throws: LocalCUError.self) { try KeyDSL.requirePlainText(input) }
        }
        try KeyDSL.requirePlainText("Caf\u{e9} \u{4f60}\u{597d} \u{1F600} 1+1=2")
    }
    @Test func kernelIdentityWorksWithoutLaunchServices() {
        let first = ProcessIdentity.startDate(pid: getpid())
        #expect(first != nil)
        #expect(first == ProcessIdentity.startDate(pid: getpid()))
        #expect(ProcessIdentity.startDate(pid: -1) == nil)
    }
}

struct DebugTapTests {
    @Test func aKeyEventRecordsNoKeyCode() throws {
        // Created, never posted.
        for down in [true, false] {
            let event = try #require(CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: down))
            let record = DebugTap.record(type: event.type, event: event)
            #expect(Set(record.keys) == ["type", "type_code", "time_seconds"])
        }
        let click = try #require(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: CGPoint(x: 3, y: 4), mouseButton: .left))
        #expect(DebugTap.record(type: .leftMouseDown, event: click)["x"] as? CGFloat == 3)
    }
}

struct DiffCacheTests {
    @MainActor @Test func theLeastRecentlyObservedWindowLeavesFirst() {
        let controller = DesktopController()
        for key in ["h", "g", "f", "e", "d", "c", "b", "a"] { controller.rememberTree([], key: key) }
        controller.rememberTree([], key: "h") // observed again, so now the newest
        controller.rememberTree([], key: "i")
        #expect(controller.previousTrees.count == 8)
        #expect(controller.previousTrees["g"] == nil)
        #expect(controller.previousTrees["a"] != nil && controller.previousTrees["h"] != nil && controller.previousTrees["i"] != nil)
    }
}

struct MarkdownPasteTests {
    @MainActor @Test func aNestedListItemHasOneMarker() throws {
        let payload = try MarkdownPaste.payload("- first\n  - nested\n- second")
        let plain = payload.first { $0.0 == .string }.map { String(decoding: $0.1, as: UTF8.self) }
        #expect(plain == "\u{2022} first\n  \u{2022} nested\n\u{2022} second")
    }
}

struct ServiceSummaryTests {
    @Test func theFirstThingThatBlocksAClientIsNamed() {
        func summary(_ change: (inout ServiceSummary.Facts) -> Void) -> ServiceSummary {
            var facts = ServiceSummary.Facts()
            change(&facts)
            return ServiceSummary(facts)
        }
        #expect(summary { _ in } == ServiceSummary(.init()))
        #expect(summary { _ in }.level == .ready && summary { _ in }.advice.isEmpty)
        #expect(summary { $0.socketReady = false; $0.accessibility = false }.level == .stopped)
        #expect(summary { $0.accessibility = false; $0.screenRecording = false; $0.paused = true }.headline == "Needs Accessibility and Screen Recording")
        #expect(summary { $0.screenRecording = false }.headline == "Needs Screen Recording")
        #expect(summary { $0.locked = true; $0.paused = true }.headline == "Desktop locked")
        #expect(summary { $0.paused = true; $0.backgroundEnabled = false }.headline == "Paused")
        #expect(summary { $0.backgroundEnabled = false }.headline == "Background control off")
        for blocked in [summary { $0.locked = true }, summary { $0.paused = true }, summary { $0.backgroundEnabled = false }] {
            #expect(blocked.level == .attention && !blocked.advice.isEmpty)
        }
    }

    @Test func theActionLogKeepsTheNewestFirstUpToItsCapacity() {
        var log = ActionLog(capacity: 3)
        for (i, tool) in ["click", "type_text", "scroll", "press_key"].enumerated() {
            log.record(tool, app: i == 0 ? nil : "Notes", at: Date(timeIntervalSince1970: Double(i)))
        }
        #expect(log.entries.map(\.tool) == ["press_key", "scroll", "type_text"])
        #expect(log.entries.first == ActionLog.Entry(at: Date(timeIntervalSince1970: 3), tool: "press_key", app: "Notes"))
    }
}
