// A small block-style YAML writer for the values the profile page sends. The
// server parses the text again and validates the result, so this only has to
// produce plain, readable YAML: strings are always double-quoted JSON strings,
// which YAML accepts, so nothing a person types can turn into a different type.

const BARE_KEY = /^[A-Za-z_][A-Za-z0-9_-]*$/;

function scalar(v: string | number | boolean | null): string {
  return typeof v === "string" ? JSON.stringify(v) : String(v);
}

function lines(v: unknown, indent: number): string[] {
  const pad = " ".repeat(indent);
  if (Array.isArray(v)) {
    if (v.length === 0) return [pad + "[]"];
    return v.flatMap((item) => {
      if (item !== null && typeof item === "object") {
        const inner = lines(item, indent + 2);
        return [pad + "- " + inner[0].slice(indent + 2), ...inner.slice(1)];
      }
      return [pad + "- " + scalar(item as string | number | boolean | null)];
    });
  }
  if (v !== null && typeof v === "object") {
    const entries = Object.entries(v).filter(([, x]) => x !== undefined);
    if (entries.length === 0) return [pad + "{}"];
    return entries.flatMap(([k, x]) => {
      const key = BARE_KEY.test(k) ? k : JSON.stringify(k);
      if (x !== null && typeof x === "object" && !(Array.isArray(x) && x.length === 0)) return [`${pad}${key}:`, ...lines(x, indent + 2)];
      return [`${pad}${key}: ${Array.isArray(x) ? "[]" : scalar(x as string | number | boolean | null)}`];
    });
  }
  return [pad + scalar(v as string | number | boolean | null)];
}

// toYaml writes a value as YAML text with no trailing newline.
export function toYaml(v: unknown): string {
  return lines(v, 0).join("\n");
}
