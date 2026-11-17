import { describe, expect, it, vi } from "vitest";
import { byText, click, mount, q } from "../testing/dom";
import { ActionMenu } from "./ActionMenu";

function setup() {
  const onDocs = vi.fn();
  const onJump = vi.fn();
  const items = [
    { label: "Jump to tab", disabled: true, title: "No tab is kept.", onSelect: onJump },
    { label: "Documents", onSelect: onDocs },
    { label: "Notes", onSelect: () => {} },
  ];
  return { onDocs, onJump, ui: <ActionMenu label="More actions: Row" items={items} /> };
}

const trigger = () => q<HTMLButtonElement>('button[aria-haspopup="menu"]')!;

describe("ActionMenu", () => {
  it("runs the chosen item once, closes, and never runs a disabled one", async () => {
    const { ui, onDocs, onJump } = setup();
    await mount(ui);
    await click(trigger());
    const jump = byText<HTMLButtonElement>('[role="menuitem"]', "Jump to tab")!;
    expect(jump.disabled).toBe(true);
    expect(jump.title).toBe("No tab is kept.");
    await click(jump);
    expect(onJump).not.toHaveBeenCalled();
    await click(byText('[role="menuitem"]', "Documents"));
    expect(onDocs).toHaveBeenCalledTimes(1);
    expect(q('[role="menu"]')).toBeNull();
  });
});
