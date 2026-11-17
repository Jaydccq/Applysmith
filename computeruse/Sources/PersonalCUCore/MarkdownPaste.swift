import AppKit

@MainActor
enum MarkdownPaste {
    static func payload(_ markdown: String) throws -> [(NSPasteboard.PasteboardType, Data)] {
        let parsed = try AttributedString(markdown: markdown)
        let rich = NSMutableAttributedString(string: "")
        let baseFont = NSFont.systemFont(ofSize: NSFont.systemFontSize)
        var previousBlock: Int?
        var previousList: Int?
        var previousRow: Int?
        for run in parsed.runs {
            try Task.checkCancellation()
            let components = run.presentationIntent?.components ?? []
            let block = components.first?.identity
            let list = components.first { $0.kind == .orderedList || $0.kind == .unorderedList }
            let row = components.first { if case .tableRow = $0.kind { return true }; return $0.kind == .tableHeaderRow }
            if rich.length > 0, block != previousBlock {
                let separator: String
                if let row, row.identity == previousRow { separator = "\t" }
                else if row != nil || previousRow != nil { separator = "\n" }
                // Items of one list, or of lists nested in each other, stay on adjacent lines.
                else if list != nil, previousList != nil { separator = "\n" }
                else { separator = "\n\n" }
                rich.append(NSAttributedString(string: separator, attributes: [.font: baseFont]))
            }
            let fragment = NSMutableAttributedString(attributedString: NSAttributedString(AttributedString(parsed[run.range])))
            var font = baseFont
            var traits: NSFontTraitMask = []
            var prefix = ""
            let isNewBlock = block != previousBlock || rich.length == 0
            // Components run innermost first; only the innermost list item gets a marker.
            var marked = false
            for component in components {
                switch component.kind {
                case .header(let level):
                    font = NSFont.systemFont(ofSize: baseFont.pointSize + CGFloat(max(1, 7 - level)) * 2)
                    traits.insert(.boldFontMask)
                case .codeBlock:
                    font = NSFont.monospacedSystemFont(ofSize: baseFont.pointSize, weight: .regular)
                case .listItem(let ordinal) where isNewBlock && !marked:
                    marked = true
                    let depth = max(0, components.filter { $0.kind == .orderedList || $0.kind == .unorderedList }.count - 1)
                    let marker = list?.kind == .orderedList ? "\(ordinal). " : "• "
                    prefix = String(repeating: "  ", count: depth) + marker + prefix
                case .blockQuote where isNewBlock:
                    prefix = "> " + prefix
                case .tableHeaderRow:
                    traits.insert(.boldFontMask)
                default: break
                }
            }
            if let intent = run.inlinePresentationIntent {
                if intent.contains(.code) { font = NSFont.monospacedSystemFont(ofSize: font.pointSize, weight: .regular) }
                if intent.contains(.stronglyEmphasized) { traits.insert(.boldFontMask) }
                if intent.contains(.emphasized) { traits.insert(.italicFontMask) }
                if intent.contains(.strikethrough) { fragment.addAttribute(.strikethroughStyle, value: NSUnderlineStyle.single.rawValue, range: NSRange(location: 0, length: fragment.length)) }
            }
            if !traits.isEmpty { font = NSFontManager.shared.convert(font, toHaveTrait: traits) }
            fragment.addAttribute(.font, value: font, range: NSRange(location: 0, length: fragment.length))
            if !prefix.isEmpty { rich.append(NSAttributedString(string: prefix, attributes: [.font: font])) }
            rich.append(fragment)
            previousBlock = block
            previousList = list?.identity
            previousRow = row?.identity
        }
        let range = NSRange(location: 0, length: rich.length)
        let rtf = try rich.data(from: range, documentAttributes: [.documentType: NSAttributedString.DocumentType.rtf])
        let html = try rich.data(from: range, documentAttributes: [.documentType: NSAttributedString.DocumentType.html])
        return [(.string, Data(rich.string.utf8)), (.rtf, rtf), (.html, html),
                (NSPasteboard.PasteboardType("net.daringfireball.markdown"), Data(markdown.utf8))]
    }
}
