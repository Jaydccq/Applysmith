import { describe, expect, it } from "vitest";
import { fail, fakeApi, ok } from "../../testing/api";
import { button, byText, click, mount, q, qa, text, type, waitFor } from "../../testing/dom";
import { health } from "../../testing/fixtures_gmail";
import { GmailSection } from "./Gmail";

function server(h = health()) {
  const api = fakeApi({
    "GET /api/gmail/health": () => ok(h),
    "GET /api/gmail/settings": () => ok({ alerts: true, scheduledDigests: false }),
  });
  return api;
}

const notConnected = () => health({ connected: false, login: "", sync: "never-run", latestRun: undefined, mailbox: undefined, cursorAgeSeconds: undefined });

describe("Settings: Gmail", () => {
  it("sends the address and the password write-only, says another account syncs from its own start, and starts a sync", async () => {
    const api = server();
    api.on("PUT /api/gmail/account", () => ok({ previous: "alex@example.com", health: health({ mailbox: "sam@example.com" }) }));
    api.on("POST /api/gmail/sync", () => ({ data: { started: true }, status: 202 }));
    await mount(<GmailSection />);
    const address = await waitFor(() => q<HTMLInputElement>('input[type="email"]'));
    await type(address, "sam@example.com");
    expect(text()).toContain("Another account than alex@example.com syncs from its own start");
    const password = q<HTMLInputElement>('input[type="password"]')!;
    await type(password, "abcdefghijklmnop");
    await click(button("Connect"));
    await waitFor(() =>
      text().includes("Gmail is connected as sam@example.com. This is a different account from alex@example.com. Mail already read stays; syncing starts again for sam@example.com. A sync has started."),
    );
    expect(api.callsTo("PUT /api/gmail/account")[0].body).toEqual({ address: "sam@example.com", password: "abcdefghijklmnop" });
    expect(password.value).toBe("");
    expect(api.callsTo("POST /api/gmail/sync")).toHaveLength(1);
  });

  it("shows a refused address or password and keeps the password off the page", async () => {
    const api = server(notConnected());
    api.on("PUT /api/gmail/account", () => fail("validation_failed", "a Google app password is 16 letters", { details: { field: "password" } }));
    await mount(<GmailSection />);
    await type(await waitFor(() => q<HTMLInputElement>('input[type="email"]')), "alex@example.com");
    const password = q<HTMLInputElement>('input[type="password"]')!;
    await type(password, "my-google-password");
    await click(button("Connect"));
    await waitFor(() => text().includes("a Google app password is 16 letters"));
    expect(password.value).toBe("");
    expect(text()).not.toContain("my-google-password");
    expect(api.callsTo("POST /api/gmail/sync")).toHaveLength(0);
  });

  it("disconnects only after the user confirms", async () => {
    const api = server();
    api.on("DELETE /api/gmail/account", () => ok(notConnected()));
    await mount(<GmailSection />);
    await click(await waitFor(() => button("Disconnect")));
    expect(api.callsTo("DELETE /api/gmail/account")).toHaveLength(0);
    await click(q<HTMLButtonElement>("dialog button.danger"));
    await waitFor(() => api.callsTo("DELETE /api/gmail/account").length === 1);
  });

  it("shows the two switches and saves a change", async () => {
    const api = server();
    api.on("PATCH /api/gmail/settings", () => ok({ alerts: true, scheduledDigests: true }));
    await mount(<GmailSection />);
    const boxes = await waitFor(() => (qa<HTMLInputElement>("input[type=checkbox]").length === 2 ? qa<HTMLInputElement>("input[type=checkbox]") : null));
    expect(boxes.map((b) => b.checked)).toEqual([false, true]);
    await click(boxes[0]);
    await waitFor(() => api.callsTo("PATCH /api/gmail/settings").length === 1);
    expect(api.callsTo("PATCH /api/gmail/settings")[0].body).toEqual({ scheduledDigests: true });
    expect(byText("p", "The product reads mail and never sends, deletes or labels it. Reply drafts stay in the product.")).not.toBeNull();
  });
});
