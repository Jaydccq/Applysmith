// Service worker: the only context that holds the token and calls the API.
import { ApiError, parseAddress } from "./api";
import * as pairing from "./pairing";
import { registerAnalyze } from "./analyze";
import { registerAdvance } from "./advance";
import { registerAnswers } from "./answers";
import { registerFill, registerJobContext } from "./fill";
import { registerPanel } from "./panel";
import { registerPress } from "./press";
import { registerDocs, registerResumes } from "./resumes";
import { listen, on } from "./router";
import { registerSession } from "./session";
import { registerSignIn } from "./signin";
import { registerSubmissions } from "./submissions";
import { api, connection, stateView } from "./state";
import * as store from "./store";
import { registerObserve, registerWidgets } from "./widgets";
import { normalizeHost } from "../shared/messages";

void store.restrictToTrusted();

on("state", () => stateView());
// The panel asks every 60 s: only the connection, never the build, cookie and preference reads of the full view.
on("health", async () => ({ connection: await connection() }));

on("pair.claim", async (m) => pairing.claimCode(api, m.code), { popupOnly: true });
on("pair.token", async (m) => pairing.pasteToken(api, m.token), { popupOnly: true });
on("pair.forget", async () => pairing.forget(), { popupOnly: true });

on(
  "settings.address",
  async (m) => {
    const r = parseAddress(m.address);
    if (!r.ok) throw new ApiError("validation_failed", r.message, 422);
    await store.put("address", r.origin);
    return r.origin;
  },
  { popupOnly: true },
);
on("settings.prefs", async (m) => store.put("prefs", { ...(await store.prefs()), ...m.prefs }), { popupOnly: true });

on("site.get", async (m) => ({ host: normalizeHost(m.host), off: await store.isOff(m.host) }));
on("site.set", async (m) => store.setOff(m.host, m.off), { popupOnly: true });
on("ui.get", async (m, s) => store.uiOf(m.host, s.tab?.id));
on("ui.set", async (m, s) => store.setUi(m.host, { pos: m.pos, open: m.open, tab: m.tab, auto: m.auto }, s.tab?.id));

registerPanel();
registerFill();
registerJobContext();
registerAdvance();
registerAnalyze();
registerAnswers();
registerResumes();
registerDocs();
registerWidgets();
registerSignIn();
registerSubmissions();
registerObserve();
registerPress();
registerSession();
listen();
