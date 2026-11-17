import { act } from "react";
import { describe, expect, it } from "vitest";
import { button, click, mount, press, q, waitFor } from "../testing/dom";
import { SaveBar } from "./SaveBar";

describe("shared table, list and form parts", () => {
  // The desktop app's contract (mayLeave in app/tray.go): true at once when
  // nothing holds the page, else only once Leave is chosen; false on Cancel,
  // and for a question that a newer one took over.
  it("answers the desktop app's leave question through the form's own dialog", async () => {
    const page = window as unknown as { applysmithHeld: () => boolean; applysmithLeave: () => Promise<boolean> };
    // The answer is wrapped: an async function would wait for it.
    const ask = async () => {
      let answer!: Promise<boolean>;
      await act(async () => void (answer = page.applysmithLeave()));
      return { answer };
    };
    await mount(<SaveBar dirty={false} savedAt=""><button type="submit">Save</button></SaveBar>);
    expect(page.applysmithHeld()).toBe(false);
    expect(await (await ask()).answer).toBe(true);
    await mount(<SaveBar dirty savedAt=""><button type="submit">Save</button></SaveBar>);
    expect(page.applysmithHeld()).toBe(true);
    const stale = await ask();
    const stay = await ask();
    expect(await stale.answer).toBe(false);
    await click(await waitFor(() => button("Cancel")));
    expect(await stay.answer).toBe(false);
    const leave = await ask();
    await click(await waitFor(() => button("Leave")));
    expect(await leave.answer).toBe(true);
  });

  it("saves with Mod+S: the unsaved form that has the keyboard, the only one otherwise, never a disabled save", async () => {
    const saved: string[] = [];
    const Form = ({ name, dirty, bad = false }: { name: string; dirty: boolean; bad?: boolean }) => (
      <form aria-label={name} onSubmit={(e) => (e.preventDefault(), saved.push(name))}>
        <input aria-label={`${name} field`} />
        <SaveBar dirty={dirty} savedAt="">
          <button type="button">Discard</button>
          <button type="submit" disabled={bad}>Save {name}</button>
        </SaveBar>
      </form>
    );
    await mount(<><Form name="policy" dirty /><Form name="notes" dirty={false} /></>);
    await press("s", { metaKey: true });
    expect(saved).toEqual(["policy"]);
    await mount(<><Form name="policy" dirty /><Form name="notes" dirty /></>);
    await press("s", { ctrlKey: true });
    expect(saved).toEqual(["policy"]);
    q<HTMLInputElement>("input[aria-label='notes field']")!.focus();
    await press("s", { ctrlKey: true }, document.activeElement!);
    expect(saved).toEqual(["policy", "notes"]);
    await mount(<Form name="policy" dirty bad />);
    await press("s", { metaKey: true });
    await mount(<Form name="policy" dirty={false} />);
    await press("s", { metaKey: true });
    expect(saved).toEqual(["policy", "notes"]);
  });
});
