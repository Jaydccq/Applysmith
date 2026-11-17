import { describe, expect, it } from "vitest";
import { toYaml } from "./yaml";

describe("toYaml", () => {
  it("quotes every string so a typed value never changes type", () => {
    expect(toYaml("true")).toBe('"true"');
    expect(toYaml("2020-01")).toBe('"2020-01"');
    expect(toYaml(["Lisbon", "Oslo"])).toBe('- "Lisbon"\n- "Oslo"');
  });

  it("writes a list of maps in block style", () => {
    expect(toYaml([{ id: "backend", default: true, tags: ["go"] }, { id: "data", default: false, tags: [] }])).toBe(
      '- id: "backend"\n  default: true\n  tags:\n    - "go"\n- id: "data"\n  default: false\n  tags: []',
    );
  });

  it("writes empty collections inline and escapes quotes and newlines", () => {
    expect(toYaml([])).toBe("[]");
    expect(toYaml('say "hi"\nthere')).toBe('"say \\"hi\\"\\nthere"');
  });
});
