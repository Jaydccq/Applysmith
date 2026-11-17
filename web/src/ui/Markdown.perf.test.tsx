import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Markdown, headingsOf } from "./Markdown";

// Each shape builds the input a hostile report could carry at size n. The
// test checks growth in CPU time rather than a wall-clock budget, so it holds on a loaded
// machine: quadrupling n roughly quadruples linear work but multiplies
// quadratic work by sixteen.
const N = 100_000;
const shapes: [string, (n: number) => string][] = [
  ["open brackets", (n) => "[".repeat(n)],
  ["image openers", (n) => "![".repeat(n / 2)],
  ["bracket run closed once", (n) => "[a".repeat(n / 2) + "]"],
  ["stars", (n) => "*".repeat(n)],
  ["double stars", (n) => "**a".repeat(n / 3)],
  ["backticks", (n) => "`".repeat(n)],
  ["unclosed link target", (n) => "x [a](" + "b".repeat(n)],
  ["link openers", (n) => "[a](".repeat(n / 4)],
  ["angle brackets closed once", (n) => "<".repeat(n) + ">"],
  ["ampersands", (n) => "&".repeat(n)],
  ["heading with many spaces", (n) => "# a" + " ".repeat(n) + "b"],
  ["heading of hashes", (n) => "# " + "#".repeat(n)],
  ["repeated headings", (n) => "## a\n".repeat(n / 5)],
  ["table separator dashes", (n) => "| a |\n|" + " --- ".repeat(n / 6) + "x"],
  ["table separator padded", (n) => "| a |\n" + " ".repeat(n) + "x"],
  ["pipes", (n) => "a|".repeat(n / 2)],
  ["list spaces", (n) => "- " + " ".repeat(n) + "x"],
  ["ordered list spaces", (n) => "1." + " ".repeat(n) + "x"],
  ["indented paragraph", (n) => " ".repeat(n) + "x"],
  ["blockquote spaces", (n) => ">" + " ".repeat(n) + "x"],
  ["rule dashes", (n) => "- ".repeat(n / 2) + "x"],
  ["fence ticks", (n) => "```".repeat(n / 3)],
  ["many paragraphs", (n) => "a\n\n".repeat(n / 3)],
];

// CPU milliseconds this process spent, which other processes on a loaded
// machine cannot inflate the way they inflate wall-clock time.
function cpuMs(): number {
  const u = process.cpuUsage();
  return (u.user + u.system) / 1000;
}

// The fastest of three runs, which filters out the remaining noise.
function bestOf3(source: string): number {
  let best = Infinity;
  for (let i = 0; i < 3; i++) {
    const t0 = cpuMs();
    renderToStaticMarkup(<Markdown source={source} />);
    headingsOf(source);
    best = Math.min(best, cpuMs() - t0);
  }
  return best;
}

describe("Markdown stays linear", () => {
  for (const [name, build] of shapes) {
    it(`renders ${name} in linear time`, () => {
      const small = bestOf3(build(N / 4));
      const large = bestOf3(build(N));
      // 2 ms of slack keeps sub-millisecond timings from producing huge ratios.
      expect(large).toBeLessThan(9 * (small + 2));
      // A hard ceiling still fails a catastrophic shape outright.
      expect(large).toBeLessThan(10_000);
    }, 60_000);
  }
});
