// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");

describe("stylesheet", () => {
  it("replaces motion with instant changes under prefers-reduced-motion", () => {
    const block = /@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? "";
    expect(block).toMatch(/\*, \*::before, \*::after, ::backdrop \{ animation: none/);
    expect(block).toMatch(/animation:\s*none/);
    expect(block).toMatch(/transition:\s*none/);
    expect(block).toMatch(/button:active:not\(:disabled\) \{ transform: none; \}/);
  });

  it("defines its colors as tokens with a dark variant for both ways of choosing dark", () => {
    expect(css).toMatch(/:root\s*\{[^}]*--bg:/);
    expect(css).toMatch(/@media \(prefers-color-scheme: dark\)\s*\{\s*:root:not\(\[data-theme="light"\]\)/);
    expect(css).toMatch(/:root\[data-theme="dark"\]/);
  });

  // The colour tokens of one theme block, read from the stylesheet.
  const tokens = (block: string): Record<string, string> =>
    Object.fromEntries(Array.from(block.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{6})\b/g), (m) => [m[1], m[2]]));
  const luminance = (hex: string) => {
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const contrast = (a: string, b: string) => {
    if (!a || !b) return 0;
    const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
    return (x + 0.05) / (y + 0.05);
  };

  it("keeps text at 4.5:1 and focus rings and field edges at 3:1 on every surface, in light and in dark", () => {
    const light = tokens(/:root \{([\s\S]*?)\n\}/.exec(css)![1]);
    const dark = tokens(/:root\[data-theme="dark"\] \{([\s\S]*?)\n\}/.exec(css)![1]);
    const auto = tokens(/:root:not\(\[data-theme="light"\]\) \{([\s\S]*?)\n  \}/.exec(css)![1]);
    expect(auto).toEqual(dark);
    const short: string[] = [];
    for (const [name, t] of [["light", light], ["dark", dark]] as const) {
      for (const surface of ["bg", "surface", "surface-subtle", "bg-sidebar"]) {
        for (const fg of ["text", "muted", "link", "accent-ink", "good", "warn", "bad", "info", "violet", "teal"]) {
          if (contrast(t[fg], t[surface]) < 4.5) short.push(`${name} ${fg} on ${surface}: ${contrast(t[fg], t[surface]).toFixed(2)}`);
        }
        for (const edge of ["focus", "control"]) {
          if (contrast(t[edge], t[surface]) < 3) short.push(`${name} ${edge} on ${surface}: ${contrast(t[edge], t[surface]).toFixed(2)}`);
        }
      }
      for (const [fg, bg] of [["good", "good-soft"], ["warn", "warn-soft"], ["bad", "bad-soft"], ["info", "info-soft"], ["accent-ink", "accent-soft"], ["violet", "violet-soft"], ["teal", "teal-soft"], ["muted", "neutral-soft"], ["accent-text", "accent"], ["primary-text", "primary"], ["primary-text", "primary-hover"], ["text", "button"], ["text", "button-hover"]]) {
        if (contrast(t[fg], t[bg]) < 4.5) short.push(`${name} ${fg} on ${bg}: ${contrast(t[fg], t[bg]).toFixed(2)}`);
      }
    }
    expect(short).toEqual([]);
  });

  it("draws the tab bar's current label in --accent-ink, as --accent is 3.4:1 on the dark surface", () => {
    expect(css).toMatch(/\.tabbar a\[aria-current="page"\] \{ color: var\(--accent-ink\);/);
  });

  it("draws the current page's sidebar icon in --accent-ink, which is 3:1 on the selected fill; --accent is 2.9:1 in dark", () => {
    expect(css).toMatch(/\.nav a\[aria-current="page"\] svg\.lucide \{ color: var\(--accent-ink\); \}/);
  });

  it("does not dim text with opacity: a stale or undone row keeps 4.5:1", () => {
    expect(css).toMatch(/\n\.stale-data \{ filter: grayscale\(1\); \}/);
    expect(css).toMatch(/\n\.item-list > li\.undone \{ color: var\(--muted\); \}/);
  });

  it("underlines a link inside running text so colour is not the only cue", () => {
    expect(css).toMatch(/:is\(main, dialog\) :is\(p, dd, li:not\(\.plain > li\), \.stat-sub, \.row > span, \.muted\) > a:not\(\[class\]\) \{ text-decoration: underline;/);
    expect(css).toMatch(/:focus-visible \{ outline: 2px solid var\(--focus\)/);
  });

  it("loads no font, image or style from another origin", () => {
    expect(css).not.toMatch(/https?:\/\//);
    expect(css).not.toMatch(/@import/);
  });

  const rule = (selector: string) => {
    const at = css.indexOf(selector + " {");
    return at < 0 ? "" : css.slice(at, css.indexOf("}", at));
  };

  it("draws checkboxes and radios itself, with the field edge colour, as WebKit's own has no 3:1 edge", () => {
    const box = /\ninput\[type="checkbox"\], input\[type="radio"\] \{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(box).toMatch(/appearance:\s*none/);
    expect(box).toMatch(/border:\s*1px solid var\(--control\)/);
    expect(css).not.toMatch(/accent-color/);
    expect(css).toMatch(/input\[type="checkbox"\]:checked, input\[type="radio"\]:checked \{ background: var\(--accent\)/);
  });

  it("makes a whole checkbox label the target, at touch size on a phone", () => {
    expect(rule(".check")).toMatch(/cursor:\s*pointer/);
    const phone = /\/\* Section controls reach touch size on a phone[\s\S]*?@media \(max-width: 767px\) \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? "";
    expect(phone).toMatch(/\.check, th button\.sort \{ min-height: 44px; \}/);
  });

  it("keeps the focus ring inside a tab, where the scrolling track cannot cut it", () => {
    expect(rule(".tabs")).toMatch(/overflow-x:\s*auto/);
    expect(rule(".tabs button")).toMatch(/outline-offset:\s*-2px/);
  });
});
