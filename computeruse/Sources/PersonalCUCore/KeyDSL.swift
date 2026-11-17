import CoreGraphics
import Foundation

struct KeyChord {
    let code: CGKeyCode
    let flags: CGEventFlags
}

enum KeyDSL {
    // Public macOS virtual key codes, corresponding to the ANSI keyboard layout.
    static let codes: [String: CGKeyCode] = [
        "a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
        "b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
        "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "equal": 24, "9": 25,
        "7": 26, "minus": 27, "8": 28, "0": 29, "rightbracket": 30, "o": 31, "u": 32,
        "leftbracket": 33, "i": 34, "p": 35, "return": 36, "l": 37, "j": 38, "quote": 39,
        "k": 40, "semicolon": 41, "backslash": 42, "comma": 43, "slash": 44, "n": 45,
        "m": 46, "period": 47, "tab": 48, "space": 49, "grave": 50, "backspace": 51,
        "escape": 53, "capslock": 57, "f17": 64, "f18": 79, "f19": 80, "f20": 90,
        "f5": 96, "f6": 97, "f7": 98, "f3": 99, "f8": 100, "f9": 101, "f11": 103,
        "f13": 105, "f16": 106, "f14": 107, "f10": 109, "f12": 111, "f15": 113,
        "help": 114, "home": 115, "pageup": 116, "delete": 117, "f4": 118, "end": 119,
        "f2": 120, "pagedown": 121, "f1": 122, "left": 123, "right": 124, "down": 125, "up": 126
    ]

    /// Typed text reaches the target as Unicode key events, and a control
    /// character among them acts as that key: "\r" or "\n" is Return. Keys are
    /// pressed only by name (press_key, submit), so typed text carries none.
    static func requirePlainText(_ text: String) throws {
        guard !text.unicodeScalars.contains(where: { $0.properties.generalCategory == .control || $0 == "\u{2028}" || $0 == "\u{2029}" }) else {
            throw LocalCUError.invalidArgument("text must not contain Return, Tab or another control character; press keys by name with press_key or submit")
        }
    }

    static func parse(_ value: String) throws -> [KeyChord] {
        let sequences = value.split(whereSeparator: { $0.isWhitespace }).map(String.init)
        guard !sequences.isEmpty, sequences.count <= 256, value.utf8.count <= 8192 else {
            throw LocalCUError.invalidArgument("keys must contain 1...256 whitespace-separated chords")
        }
        return try sequences.map { sequence in
            let normalized = sequence == "+" ? "plus" : sequence.hasSuffix("++") ? String(sequence.dropLast()) + "plus" : sequence
            let rawComponents = normalized.split(separator: "+", omittingEmptySubsequences: false).map(String.init)
            let components = rawComponents.map { $0.lowercased() }
            guard let last = components.last, !last.isEmpty else { throw LocalCUError.invalidArgument("Invalid key chord: \(sequence)") }
            var flags: CGEventFlags = []
            for modifier in components.dropLast() {
                switch modifier {
                case "command", "cmd", "super", "meta": flags.insert(.maskCommand)
                case "shift": flags.insert(.maskShift)
                case "option", "alt": flags.insert(.maskAlternate)
                case "control", "ctrl": flags.insert(.maskControl)
                default: throw LocalCUError.invalidArgument("Unknown modifier: \(modifier)")
                }
            }
            let aliases = ["enter": "return", "esc": "escape", "arrowleft": "left", "arrowright": "right", "arrowup": "up", "arrowdown": "down", "del": "delete"]
            var key = aliases[last] ?? last
            if let raw = rawComponents.last, raw.count == 1, raw.first!.isLetter, raw == raw.uppercased() { flags.insert(.maskShift) }
            let punctuation: [String: String] = ["=": "equal", "-": "minus", "[": "leftbracket", "]": "rightbracket", "'": "quote", ";": "semicolon", "\\": "backslash", ",": "comma", "/": "slash", ".": "period", "`": "grave"]
            key = punctuation[key] ?? key
            let shifted: [String: String] = ["!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0", "_": "minus", "{": "leftbracket", "}": "rightbracket", "\"": "quote", ":": "semicolon", "|": "backslash", "<": "comma", "?": "slash", ">": "period", "~": "grave"]
            if let base = shifted[key] { key = base; flags.insert(.maskShift) }
            if key == "plus" { key = "equal"; flags.insert(.maskShift) }
            guard let code = codes[key] else { throw LocalCUError.invalidArgument("Unknown key: \(last); use named punctuation such as plus, minus, comma") }
            return KeyChord(code: code, flags: flags)
        }
    }
}
