// expectAccessible runs axe-core over the rendered document and fails on any
// serious or critical violation. The rules that measure layout or computed
// colour are left to the browser audit (docs/architecture/dashboard.md):
// happy-dom draws nothing and loads no stylesheet, so they would only guess.
// A frame (the PDF preview) is checked as an element, its title included; its
// document is the browser's viewer, and happy-dom cannot carry axe into it.
import axe from "axe-core";
import { expect } from "vitest";

const LAYOUT_RULES = ["color-contrast", "color-contrast-enhanced", "link-in-text-block", "target-size", "scrollable-region-focusable", "region"];

export async function expectAccessible(root: Element = document.body): Promise<void> {
  const result = await axe.run(root, {
    resultTypes: ["violations"],
    iframes: false,
    rules: Object.fromEntries(LAYOUT_RULES.map((id) => [id, { enabled: false }])),
  });
  const serious = result.violations
    .filter((v) => v.impact === "serious" || v.impact === "critical")
    .map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.html.slice(0, 160)).join(" | ")}`);
  expect(serious).toEqual([]);
}
