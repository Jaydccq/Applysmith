import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { click, mount, press, q, qa, settle } from "../testing/dom";
import { expectAccessible } from "../testing/axe";
import { keepFocus } from "./focus";
import { ConfirmDialog, Modal } from "./Modal";

function Opener({ drop }: { drop?: boolean }) {
  const [open, setOpen] = useState(false);
  const [gone, setGone] = useState(false);
  return (
    <main id="main" tabIndex={-1}>
      {!gone && (
        <button type="button" id="opener" onClick={() => setOpen(true)}>
          Open row
        </button>
      )}
      {open && (
        <Modal
          title="Row"
          variant="drawer"
          onClose={() => {
            if (drop) setGone(true);
            setOpen(false);
          }}
        >
          <label>
            Note
            <input id="note" />
          </label>
          <button type="button" id="save">
            Save
          </button>
        </Modal>
      )}
    </main>
  );
}

const active = () => document.activeElement as HTMLElement | null;

describe("drawer and dialog focus", () => {
  it("starts on the first control, wraps Tab and Shift+Tab inside, closes on Escape and returns focus to its opener", async () => {
    await mount(<Opener />);
    q<HTMLButtonElement>("#opener")!.focus();
    await click(q("#opener"));
    const dialog = q("dialog")!;
    expect(active()?.getAttribute("aria-label")).toBe("Close");
    await expectAccessible();

    q<HTMLButtonElement>("#save")!.focus();
    await press("Tab", {}, q("#save")!);
    expect(active()?.getAttribute("aria-label")).toBe("Close");
    await press("Tab", { shiftKey: true }, active()!);
    expect(active()?.id).toBe("save");
    expect(dialog.contains(active())).toBe(true);

    await press("Escape", {}, active()!);
    expect(q("dialog")).toBeNull();
    expect(active()?.id).toBe("opener");
  });

  it("puts the first focus of a confirmation on Cancel, and only the innermost dialog answers a key", async () => {
    const outer = vi.fn();
    const inner = vi.fn();
    await mount(
      <Modal title="Outer" variant="drawer" onClose={outer}>
        <ConfirmDialog title="Remove the row?" consequence="It is removed." verb="Remove" onConfirm={() => {}} onCancel={inner} />
      </Modal>,
    );
    const [, confirm] = qa("dialog");
    expect(active()?.textContent).toBe("Cancel");
    await press("Escape", {}, active()!);
    expect(inner).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
    const remove = confirm.querySelector<HTMLButtonElement>("button.danger")!;
    remove.focus();
    await press("Tab", {}, remove);
    expect(active()?.textContent).toBe("Cancel");
  });
});

function List({ rows }: { rows: string[] }) {
  return (
    <ul>
      {rows.map((r) => (
        <li key={r}>
          {r} <button type="button">Dismiss {r}</button>
        </li>
      ))}
    </ul>
  );
}

describe("keepFocus", () => {
  async function setup(rows: string[]) {
    const host = await mount(<List rows={rows} />);
    const main = document.createElement("main");
    main.id = "main";
    main.tabIndex = -1;
    document.body.insertBefore(main, host);
    main.appendChild(host);
    const stop = keepFocus(main);
    return { main, stop };
  }

  it("moves focus to the next row when the row whose button had it leaves the list", async () => {
    const { stop } = await setup(["a", "b", "c"]);
    const b = qa<HTMLButtonElement>("button")[1];
    b.focus();
    b.closest("li")!.remove();
    await settle();
    expect(active()?.textContent).toBe("Dismiss c");
    stop();
  });
});
