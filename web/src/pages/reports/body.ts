// A report file opens with key: value lines (report, date, score, ...), a blank
// line, then a title and the Markdown. The header and the title are already
// shown above the body, and Markdown would run the header lines into one
// paragraph.
const HEADER_LINE = /^[a-z][a-z_]*: ?.*$/;

export function reportBody(content: string): string {
  if (!content.startsWith("report: ")) return content;
  const lines = content.split("\n");
  let end = 0;
  while (end < lines.length && HEADER_LINE.test(lines[end])) end++;
  return lines.slice(end).join("\n").replace(/^\n*# [^\n]*\n+/, "");
}

// payOf is the pay the posting states, from the Snapshot's "Pay as stated"
// line ("not stated" included), or "" for a report without that line.
export function payOf(content: string): string {
  return /^- Pay as stated: (.+)$/m.exec(content)?.[1].trim() ?? "";
}
