import { describe, expect, it } from "vitest";
import { fakeApi, ok } from "../../testing/api";
import { button, click, mount, qa, submit, type, waitFor } from "../../testing/dom";
import { source } from "../../testing/fixtures_discovery";
import { SourceKeys } from "./SourceKeys";

const keyField = (label: RegExp) => qa<HTMLInputElement>("input").find((i) => i.labels?.[0]?.textContent && label.test(i.labels[0].textContent))!;
const sources = [
  source({ name: "theirstack", title: "TheirStack", needs: "key", keyName: "api-key-theirstack", readiness: "needs key" }),
  source({ name: "a16z", title: "a16z portfolio jobs" }),
];

describe("Settings: Scan source API keys", () => {
  it("stores a keyed source's key under its vault name, empties the field and shows it saved, and lists only keyed sources", async () => {
    const KEY = ["source", "canary", "3"].join("-");
    let present = false;
    const api = fakeApi({
      "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "api-key-theirstack", kind: "api_key", present }] }),
      "PUT /api/secrets/api-key-theirstack": () => {
        present = true;
        return ok({ name: "api-key-theirstack", present: true, source: "keychain" });
      },
    });
    await mount(<SourceKeys sources={sources} />);
    const field = await waitFor(() => keyField(/TheirStack API key/));
    expect(field.labels?.[0]?.textContent).toContain("not set");
    expect(qa("input")).toHaveLength(1);
    await type(field, KEY);
    await submit(field.closest("form"));
    await waitFor(() => api.callsTo("PUT /api/secrets/api-key-theirstack").length === 1);
    expect(api.callsTo("PUT /api/secrets/api-key-theirstack")[0].body).toEqual({ value: KEY });
    expect(field.value).toBe("");
    await waitFor(() => keyField(/TheirStack API key/).labels?.[0]?.textContent?.includes("saved") === true);
    expect(document.body.innerHTML).not.toContain(KEY);
  });

  it("removes a saved key only after the dialog is confirmed", async () => {
    let present = true;
    const api = fakeApi({
      "GET /api/secrets": () => ok({ registry: [], entries: [{ name: "api-key-theirstack", kind: "api_key", present }] }),
      "DELETE /api/secrets/api-key-theirstack": () => {
        present = false;
        return ok({ name: "api-key-theirstack", present: false });
      },
    });
    await mount(<SourceKeys sources={sources} />);
    await click(await waitFor(() => button("Remove key")));
    expect(api.callsTo("DELETE /api/secrets/api-key-theirstack")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Remove key")!);
    await waitFor(() => api.callsTo("DELETE /api/secrets/api-key-theirstack").length === 1);
    await waitFor(() => keyField(/TheirStack API key/).labels?.[0]?.textContent?.includes("not set") === true);
  });
});
