// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Scan cost and overlays.
import { expect, test } from "vitest";
import { scanControls } from "../src/scan";
import { mount } from "./helpers";

test("Many controls: four times the controls cost under six times the document reads, so no control reads the whole page", () => {
  const work = (n: number) => {
    mount(`<form>${Array.from({ length: n }, (_, i) => `<div><label for="q${i}">Question ${i}</label><input id="q${i}"></div>`).join("")}</form>`);
    const size = document.getElementsByTagName("*").length;
    const own = document.querySelectorAll.bind(document);
    let reads = 0;
    // Every query of the document walks all of it. happy-dom's own stylesheet lookup behind getComputedStyle is not the engine's.
    (document as unknown as { querySelectorAll(s: string): NodeListOf<Element> }).querySelectorAll = (s: string) => {
      if (s !== 'style,link[rel="stylesheet"]') reads += size;
      return own(s);
    };
    try {
      expect(scanControls(document)).toHaveLength(n);
    } finally {
      delete (document as unknown as { querySelectorAll?: unknown }).querySelectorAll;
    }
    return reads;
  };
  const small = work(100);
  expect(work(400)).toBeLessThan(6 * small);
});
