import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { button, click, mount, q, qa, submit, text, type, waitFor } from "../../testing/dom";
import { TelegramSection } from "./Telegram";

const CANARY = ["123456", "FAKE", "canary"].join(":");
const idle = { tokenSet: false, paired: false, health: "stopped", unauthorizedAttempts: 0 };

const NOW = Date.parse("2026-10-12T10:00:00Z");

function server(initial: Record<string, unknown> = idle) {
  let status = initial;
  const api = fakeApi({
    "GET /api/telegram/status": () => ok(status),
    "PUT /api/telegram/token": () => ok((status = { ...status, tokenSet: true, botUsername: "example_bot", health: "configured" })),
    "DELETE /api/telegram/token": () => ok((status = { ...status, tokenSet: false, health: "stopped" })),
    // The server fills the link and its QR code whenever it knows the bot's name.
    "POST /api/telegram/pairing-code": () => ok({ code: "482913", botUsername: "example_bot", expiresAt: new Date(NOW + 60_000).toISOString(), link: "https://t.me/example_bot?start=482913", qr: { size: 2, rows: ["10", "01"] } }),
    "DELETE /api/telegram/pairing": () => ok((status = { ...status, paired: false })),
  });
  return api;
}

describe("Settings: Telegram", () => {
  it("sends the token write-only, empties the field at once and never renders it", async () => {
    const api = server();
    await mount(<TelegramSection />);
    const field = await waitFor(() => q<HTMLInputElement>("input[type=password]"));
    expect(field.getAttribute("autocomplete")).toBe("new-password");
    await type(field, CANARY);
    await submit(field.closest("form"));
    await waitFor(() => api.callsTo("PUT /api/telegram/token").length === 1);
    expect(api.callsTo("PUT /api/telegram/token")[0].body).toEqual({ token: CANARY });
    expect(field.value).toBe("");
    await waitFor(() => text().includes("Saved at"));
    expect(document.body.innerHTML).not.toContain(CANARY);
    expect(text()).toContain("bot token".replace("bot", "Bot"));
  });

  it("removes the token only after a confirmation", async () => {
    const api = server({ ...idle, tokenSet: true, health: "healthy" });
    await mount(<TelegramSection />);
    await click(await waitFor(() => button("Remove token")));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/telegram/token")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Remove token")!);
    await waitFor(() => api.callsTo("DELETE /api/telegram/token").length === 1);
  });

  it("offers a link and a QR code that pair the chat with one press of Start", async () => {
    const link = "https://t.me/example_bot?start=482913";
    const api = fakeApi({
      "GET /api/telegram/status": () => ok({ ...idle, tokenSet: true, botUsername: "example_bot", health: "healthy" }),
      "POST /api/telegram/pairing-code": () => ok({ code: "482913", botUsername: "example_bot", expiresAt: new Date(Date.now() + 60_000).toISOString(), link, qr: { size: 2, rows: ["10", "01"] } }),
    });
    await mount(<TelegramSection />);
    await click(await waitFor(() => button("Get a pairing code")));
    await waitFor(() => api.callsTo("POST /api/telegram/pairing-code").length === 1);
    const open = await waitFor(() => q<HTMLAnchorElement>(`a[href="${link}"]`));
    expect(open.textContent).toBe("Open @example_bot in Telegram");
    expect(q("svg[role=img]")?.getAttribute("aria-label")).toContain("Telegram");
    expect(q(".pairing-code")?.textContent).toBe("482913");
    expect(text()).not.toContain("/newbot");
  });

  it("unpairs the chat only after a confirmation", async () => {
    const api = server({ tokenSet: true, botUsername: "example_bot", paired: true, health: "healthy", unauthorizedAttempts: 0 });
    await mount(<TelegramSection />);
    await click(await waitFor(() => button("Unpair this chat")));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/telegram/pairing")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Unpair")!);
    await waitFor(() => api.callsTo("DELETE /api/telegram/pairing").length === 1);
    await waitFor(() => button("Get a pairing code"));
  });
});
