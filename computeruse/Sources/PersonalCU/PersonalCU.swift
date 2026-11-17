import Foundation
import AppKit
import ApplicationServices
import MCP
import PersonalCUCore

@main
struct PersonalCU {
    @MainActor static func main() {
        let command = CommandLine.arguments.dropFirst().first ?? "service"
        if command == "button-guard" { SystemButtonHold.guardMain(); return }
        if command == "mcp" {
            do { try RuntimeEndpoint.relay() } catch { fail(error) }
            return
        }
        let app = NSApplication.shared
        if command == "service" {
            let delegate = ManagerDelegate(showOnLaunch: CommandLine.arguments.count == 1 || ProcessInfo.processInfo.environment["PERSONAL_CU_SHOW_MANAGER"] == "1")
            app.delegate = delegate
            withExtendedLifetime(delegate) { app.run() }
            return
        }
        app.setActivationPolicy(.prohibited)
        Task { @MainActor in
            do {
                if command == "standalone-mcp" { try await MCPHost().run(transport: LocalTransport()) }
                else if command == "doctor" || command == "list-apps" {
                    let result = try await MCPHost().invoke(command == "doctor" ? "doctor" : "list_apps", arguments: [:])
                    print(String(decoding: try JSONSerialization.data(withJSONObject: result.data, options: [.prettyPrinted, .sortedKeys]), as: UTF8.self))
                } else if command == "permissions" {
                    let accessibility = AXIsProcessTrustedWithOptions([kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary)
                    let screen = CGRequestScreenCaptureAccess()
                    let monitoring = CGRequestListenEventAccess()
                    print("Accessibility: \(accessibility); screen recording: \(screen); input monitoring: \(monitoring). Relaunch after newly granting access.")
                } else if command == "login-enable" {
                    try LoginService.service.register(); print(LoginService.status)
                } else if command == "login-disable" {
                    try await LoginService.service.unregister(); print(LoginService.status)
                } else if command == "login-status" { print(LoginService.status) }
                else { print("PersonalCU 0.2.0\nUsage: personal-cu service | mcp | doctor | list-apps | permissions | login-enable | login-disable | login-status | standalone-mcp") }
            } catch { fail(error) }
            app.terminate(nil)
        }
        app.run()
    }
    static func fail(_ error: Error) -> Never {
        FileHandle.standardError.write(Data((error.localizedDescription + "\n").utf8))
        exit(1)
    }
}
