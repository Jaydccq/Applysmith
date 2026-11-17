import { act } from "react";
import { describe, expect, it } from "vitest";
import { BoardContext } from "../hooks";
import { fail, fakeApi, manualBoard, ok, type FakeApi } from "../testing/api";
import { button, byLabel, byText, click, mount, q, qa, submit, text, type, waitFor } from "../testing/dom";
import { bankFile, candidate, config, entry, gap, profileValues, settings, unanswered, usageRow } from "../testing/fixtures_pages";
import type { AnswerBank as Bank, AnswerBankDerived, AnswerBankEntry, AnswerBankShape, AnswerCandidate } from "../wire";
import { today } from "../ui/parts";
import AnswerBank from "./AnswerBank";

interface State {
  entries: AnswerBankEntry[];
  shapes: Record<string, AnswerBankShape>;
  candidates: AnswerCandidate[];
  legal: boolean;
  derived: AnswerBankDerived[];
  profile: { [key: string]: unknown };
}

// A scripted server that keeps the bank, so a save is visible to the next read
// the way it is on the real one. The needs-answer rule is the simple one: a
// fixed entry with no answer needs it, and the ask-me marker is a decision.
function server(init: Partial<State> = {}): { api: FakeApi; state: State; elsewhere: (change: () => void) => void } {
  const state: State = { entries: [entry()], shapes: {}, candidates: [], legal: false, derived: [], profile: profileValues(), ...init };
  let revision = 1;
  const askMe = () => state.entries.filter((e) => e.answer === "@ask-me").map((e) => e.id);
  const open = () => state.entries.filter((e) => e.kind === "fixed" && !e.answer && !state.derived.some((d) => d.id === e.id));
  const api = fakeApi({
    "GET /api/answer-bank": () => ok({ ...bankFile(state.entries, `rev-${revision}`), shapes: state.shapes }),
    "PUT /api/answer-bank": (c) => {
      const body = c.body as { expectedRevision: string; bank: { entries: AnswerBankEntry[] } };
      if (body.expectedRevision !== `rev-${revision}`) return fail("revision_conflict", "answer-bank.yml changed on disk; reload it and apply the change again");
      state.entries = body.bank.entries;
      revision++;
      return ok({ revision: `rev-${revision}`, changed: true });
    },
    "GET /api/answer-bank/unanswered": () =>
      ok(unanswered({ unanswered: open().map((e) => gap(e.id, e.id === "email-address")), blocking: open().filter((e) => e.id === "email-address").map((e) => gap(e.id, true)), askMe: askMe(), derived: state.derived })),
    "GET /api/answer-bank/candidates": () => ok({ candidates: state.candidates }),
    "GET /api/answer-bank/usage": () => ok({ windowDays: 30, rows: [usageRow()] }),
    "GET /api/answer-bank/settings": () => ok({ ...settings, autoAcceptLegalDeclarations: state.legal }),
    "GET /api/config/profile": () => ok(config("profile", { values: state.profile })),
    "PATCH /api/config/profile": () => ok({ revision: "profile-rev-2", changed: true }),
    "PATCH /api/answer-bank/settings": (c) => {
      state.legal = (c.body as { autoAcceptLegalDeclarations: boolean }).autoAcceptLegalDeclarations;
      return ok({ ...settings, autoAcceptLegalDeclarations: state.legal });
    },
  });
  // elsewhere is a write the page did not make (Ask Agent, a run, the CLI).
  return { api, state, elsewhere: (change) => (change(), revision++) };
}

const field = <T extends HTMLElement = HTMLInputElement>(label: string) => qa<T>("dialog.drawer :is(input, textarea, select)").find((el) => (el as unknown as HTMLInputElement).labels?.[0]?.textContent === label)!;
const confirm = (verb: string) => click(byText("dialog:not(.drawer) button", verb));
const openTab = async (name: string) => click(byText("button[role=tab]", new RegExp(`^${name}`)));
const openEntry = async (label: string) => click(await waitFor(() => button(label)));

describe("Answer Bank: entries", () => {

  it("saves the whole bank against the revision it loaded and shows the new answer", async () => {
    const { api, state } = server();
    await mount(<AnswerBank />);
    await openEntry("Authorized to work");
    await type(qa<HTMLTextAreaElement>("textarea").find((t) => t.labels?.[0]?.textContent === "Answer")!, "No");
    await click(button("Save answer"));
    await waitFor(() => api.callsTo("PUT /api/answer-bank").length === 1);
    expect((api.callsTo("PUT /api/answer-bank")[0].body as { expectedRevision: string }).expectedRevision).toBe("rev-1");
    expect(state.entries[0].answer).toBe("No");
  });

  it("offers a reload when the bank changed under the editor, and keeps the edit through it", async () => {
    const { api } = server();
    await mount(<AnswerBank />);
    await openEntry("Authorized to work");
    const answer = () => qa<HTMLTextAreaElement>("textarea").find((t) => t.labels?.[0]?.textContent === "Answer")!;
    await type(answer(), "No");
    api.on("PUT /api/answer-bank", () => fail("revision_conflict", "answer-bank.yml changed on disk; reload it and apply the change again"));
    await click(button("Save answer"));
    await waitFor(() => text().includes("changed on disk"));
    api.on("GET /api/answer-bank", () => ok(bankFile([entry()], "rev-9")));
    api.on("PUT /api/answer-bank", () => ok({ revision: "rev-10", changed: true }));
    await click(button("Reload the bank, keeping your edits"));
    await waitFor(() => api.callsTo("GET /api/answer-bank").length === 2);
    expect(answer().value).toBe("No");
    await click(button("Save answer"));
    await waitFor(() => api.callsTo("PUT /api/answer-bank").length === 2);
    expect(api.callsTo("PUT /api/answer-bank")[1].body).toMatchObject({ expectedRevision: "rev-9" });
    await waitFor(() => text().includes("Saved: Authorized to work."));
  });

  it("Pinned entry: its answer is still the user's to edit, since a pin holds back only automatic changes", async () => {
    const { state } = server({ entries: [entry({ pinned: true })] });
    await mount(<AnswerBank />);
    await openEntry("Authorized to work");
    await type(qa<HTMLTextAreaElement>("textarea").find((t) => t.labels?.[0]?.textContent === "Answer")!, "No");
    await click(button("Save answer"));
    await waitFor(() => state.entries[0].answer === "No");
    expect(state.entries[0].pinned).toBe(true);
  });
  it("Ask-me marker is a decision: leaves both needs-answer counts", async () => {
    const { state } = server({ entries: [entry({ id: "street", description: "Street address", category: "contact", answer: "" }), entry({ id: "email-address", description: "Email", category: "contact", answer: "x" })] });
    await mount(<AnswerBank />);
    const chip = (label: string) => byText(".filter-chips a", new RegExp(`^${label}`));
    await waitFor(() => chip("To answer")?.textContent === "To answer 1");
    await openEntry("Street address");
    await click(button("Ask me each time"));
    expect(text()).toContain("You chose to be asked each time");
    expect(button("Ask me each time")).toBeNull();
    await click(button("Save answer"));
    await waitFor(() => chip("To answer")?.textContent === "To answer 0");
    expect(chip("Ask me")?.textContent).toBe("Ask me 1");
    expect(state.entries[0].answer).toBe("@ask-me");
    expect(byText("ul.entry-list .chip", "Ask me")).not.toBeNull();
  });
});

describe("Answer Bank: questions runs wait on", () => {
  // Two runs reach the blank County entry, one asks a question no entry holds.
  const county = { question: "What county do you live in?", kind: "text", entryId: "address-county", runId: "run_b", host: "jobs.example.com", runs: 2, everywhere: true };
  const tool = { question: "Favourite tool", kind: "text", runId: "run_a", host: "jobs.example.com", runs: 1, everywhere: true };

  it("heads the to-answer view with each question once and how many runs wait on it, and saves an answer through the newest run's form", async () => {
    const { api, state } = server({ entries: [entry(), entry({ id: "address-county", description: "County", category: "contact", answer: "" }), entry({ id: "email-address", description: "Email", category: "contact", answer: "" })] });
    let answered = false;
    api.on("GET /api/answer-bank/unanswered", () => {
      const open = state.entries.filter((e) => !e.answer).map((e) => gap(e.id));
      return ok(unanswered({ unanswered: open, asked: answered ? [county] : [county, tool] }));
    });
    api.on("POST /api/apply/runs/run_a/answers", () => {
      answered = true;
      return ok({ entryId: "custom-favourite-tool" });
    });
    window.history.replaceState(null, "", "/answer-bank?show=needs");
    await mount(<AnswerBank />);
    const section = await waitFor(() => q("section[aria-labelledby='ab-asked']"));
    expect(qa(".filter-chips a").find((a) => /^To answer/.test(a.textContent ?? ""))?.textContent).toBe("To answer 3");
    expect(qa("section[aria-labelledby='ab-asked'] .q").map((p) => p.textContent)).toEqual(["What county do you live in?", "Favourite tool"]);
    expect(section.textContent).toContain("2 runs wait on this answer.");
    expect(section.textContent).toContain("1 run waits on this answer.");
    // County is the first question, so its blank entry is not listed again.
    expect(qa("ul.entry-list li").map((li) => li.querySelector(".entry-open")?.textContent)).toEqual(["Email"]);
    const row = qa("section[aria-labelledby='ab-asked'] li")[1];
    await type(row.querySelector<HTMLInputElement>("input:not([type])")!, "Go");
    await click(row.querySelector<HTMLButtonElement>("button[type='submit']")!);
    await waitFor(() => api.callsTo("POST /api/apply/runs/run_a/answers").length === 1);
    expect(api.callsTo("POST /api/apply/runs/run_a/answers")[0].body).toEqual({ question: "Favourite tool", answer: "Go", everywhere: true });
    await waitFor(() => qa("section[aria-labelledby='ab-asked'] .q").length === 1);
    expect(text()).toContain("Saved: Favourite tool.");
    // Its run kept no tab, so it went nowhere: the note leads to the run, where it is retried.
    expect(byText("p[role=status] a", "Open the run to retry it")?.getAttribute("href")).toBe("/runs/run_a");
    window.history.replaceState(null, "", "/answer-bank");
  });

});

describe("Answer Bank: answering and deleting entries", () => {
  it("deletes an entry only after a confirmation and puts it back in place with Undo", async () => {
    const { state } = server({ entries: [entry({ id: "a", description: "First" }), entry({ id: "b", description: "Second" }), entry({ id: "c", description: "Third" })] });
    await mount(<AnswerBank />);
    await openEntry("Second");
    await click(byText("dialog.drawer .actions button", "Delete entry"));
    expect(state.entries).toHaveLength(3);
    await confirm("Delete entry");
    await waitFor(() => text().includes("Deleted: Second."));
    expect(state.entries.map((e) => e.id)).toEqual(["a", "c"]);
    expect(q("dialog.drawer")).toBeNull();
    await click(button("Undo"));
    await waitFor(() => text().includes("Restored: Second."));
    expect(state.entries.map((e) => e.id)).toEqual(["a", "b", "c"]);
  });

  it("Undo after a change made elsewhere puts the entry back into the bank as it is now, and says why when its id was taken", async () => {
    const { api, state, elsewhere } = server({ entries: [entry({ id: "a", description: "First" }), entry({ id: "b", description: "Second" }), entry({ id: "c", description: "Third" })] });
    await mount(<AnswerBank />);
    const remove = async (name: string) => {
      await openEntry(name);
      await click(byText("dialog.drawer .actions button", "Delete entry"));
      await confirm("Delete entry");
      await waitFor(() => text().includes(`Deleted: ${name}.`));
    };
    await remove("Second");
    elsewhere(() => state.entries.push(entry({ id: "d", description: "Fourth" })));
    await click(button("Undo"));
    await waitFor(() => text().includes("Restored: Second."));
    expect(state.entries.map((e) => e.id)).toEqual(["a", "b", "c", "d"]);
    await remove("Third");
    elsewhere(() => state.entries.push(entry({ id: "c", description: "Another third" })));
    const puts = api.callsTo("PUT /api/answer-bank").length;
    await click(button("Undo"));
    await waitFor(() => text().includes("Not restored: the bank has an entry with the id c again."));
    expect(api.callsTo("PUT /api/answer-bank")).toHaveLength(puts);
  });

  it("answers within the entry's shape: a yes or no switch, a date field, and a choice that writes every wording forms use", async () => {
    const notice = [
      { label: "Immediately", matches: ["Immediately", "None"] },
      { label: "2 weeks", matches: ["2 weeks", "Two weeks"] },
      { label: "1 month", matches: ["1 month", "One month", "4 weeks"] },
    ];
    const { api, state } = server({
      entries: [
        entry({ id: "relocate", description: "Willing to relocate", answer: "Yes, for the right role", input_types: ["yes_no"] }),
        entry({ id: "start", description: "Start date", answer: "", input_types: ["date"] }),
        entry({ id: "notice", description: "Notice period", kind: "pick-option", answer: undefined, preferred: ["Two weeks notice"], fallback_text: "" }),
      ],
      shapes: { relocate: { type: "yes-no", choices: [] }, start: { type: "date", choices: [] }, notice: { type: "choice", choices: notice } },
    });
    await mount(<AnswerBank />);
    await openEntry("Willing to relocate");
    // An answer a form taught that is no choice shows until a pick replaces it.
    expect(q("dialog.drawer fieldset.choice")?.textContent).toContain("Now: \u201CYes, for the right role\u201D, from a form.");
    expect(qa("dialog.drawer textarea")).toHaveLength(0);
    await click(byText("dialog.drawer .segmented label", "No")!.querySelector("input"));
    await click(button("Save answer"));
    await waitFor(() => state.entries[0].answer === "No");
    await openEntry("Start date");
    expect(field("Answer").type).toBe("date");
    await openEntry("Notice period");
    expect(text()).not.toContain("Preferred options");
    expect(text()).not.toContain("Choose any option");
    await click(byText("dialog.drawer .segmented label", "1 month")!.querySelector("input"));
    await click(button("Save answer"));
    await waitFor(() => api.callsTo("PUT /api/answer-bank").length === 2);
    expect(state.entries[2]).toMatchObject({ preferred: ["1 month", "One month", "4 weeks"], fallback_text: "1 month" });
  });
});

// A bank held whole, employers and overrides too.
function wholeBank(api: FakeApi, init: Partial<Bank>, defaultIds: string[] = []) {
  const held = { bank: { version: 1, employers: [], entries: [entry()], overrides: {}, ...init } as Bank, rev: 1 };
  api.on("GET /api/answer-bank", () => ok({ ...bankFile(held.bank.entries, `rev-${held.rev}`), bank: held.bank, defaultIds }));
  api.on("PUT /api/answer-bank", (c) => {
    held.bank = (c.body as { bank: Bank }).bank;
    held.rev++;
    return ok({ revision: `rev-${held.rev}`, changed: true });
  });
  return held;
}
const input = (label: RegExp) => qa<HTMLInputElement>("input, textarea").find((el) => label.test(el.labels?.[0]?.textContent ?? ""))!;

describe("Answer Bank: employers and shipped defaults", () => {
  it("edits the employer list: adds a company with its other names, removes one, refuses a nameless one and saves the whole bank", async () => {
    const { api } = server();
    const held = wholeBank(api, { employers: [{ name: "Contoso", aliases: ["Contoso Ltd"] }, { name: "Fabrikam", aliases: [] }] });
    await mount(<AnswerBank />);
    await openTab("Employers");
    await waitFor(() => input(/^Company of employer 1/)?.value === "Contoso");
    expect(input(/^Other names.* of employer 1/).value).toBe("Contoso Ltd");
    expect(text()).toContain("Have you ever worked for");
    expect(button("Save employers")?.disabled).toBe(true);
    await click(byLabel(/Remove employer 2/));
    await click(button("Add employer"));
    await type(input(/^Other names.* of employer 2/), "Northwind Traders, NWT");
    expect(text()).toContain("Each employer needs a company name.");
    expect(button("Save employers")?.disabled).toBe(true);
    await type(input(/^Company of employer 2/), "Northwind");
    await click(button("Save employers"));
    await waitFor(() => text().includes("Saved at"));
    expect(held.bank.employers).toEqual([{ name: "Contoso", aliases: ["Contoso Ltd"] }, { name: "Northwind", aliases: ["Northwind Traders", "NWT"] }]);
    expect(held.bank.entries).toHaveLength(1);
    expect(api.callsTo("PUT /api/answer-bank")[0].body).toMatchObject({ expectedRevision: "rev-1" });
  });

  it("keeps the companies a relative works at apart from your own, in the profile's relationships, and a save keeps the other lists", async () => {
    const friend = { family: "friend", company: "Contoso Example", recorded: "2027-03-04" };
    const { api } = server({ profile: profileValues({ company_relationships: { relationships: [friend] } }) });
    await mount(<AnswerBank />);
    await openTab("Employers");
    await waitFor(() => button("Add relative"));
    expect([...qa("h2")].map((h) => h.textContent)).toEqual(expect.arrayContaining(["Companies you worked for", "Companies where a relative works"]));
    expect(button("Add friend")).toBeNull();
    await click(button("Add relative"));
    await type(input(/^Company of relative 1/), "Northwind");
    await type(input(/^Relation of relative 1/), "Sibling");
    await click(button("Save relationships"));
    await waitFor(() => api.callsTo("PATCH /api/config/profile").length === 1);
    expect((api.callsTo("PATCH /api/config/profile")[0].body as { ops: unknown[] }).ops).toEqual([
      {
        op: "set",
        path: "company_relationships.relationships",
        value: [`- family: "relative"\n  company: "Northwind"\n  relation: "Sibling"\n  recorded: "${today()}"`, '- family: "friend"\n  company: "Contoso Example"\n  recorded: "2027-03-04"'].join("\n"),
      },
    ]);
  });

});

describe("Answer Bank: candidates", () => {
  it("never approves on its own: nothing is sent until the user presses Approve", async () => {
    const { api } = server({ candidates: [candidate()] });
    await mount(<AnswerBank />);
    await waitFor(() => button("Suggestions (1)"));
    await openTab("Suggestions");
    await waitFor(() => text().includes("How did you hear about Example Corp?"));
    expect(text()).toContain("3 occurrences");
    expect(text()).toContain("jobs.example.com");
    expect(text()).toContain("A colleague");
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")).toHaveLength(0);
  });

  it("Reject a candidate: it leaves the queue, and Undo brings it back", async () => {
    const { api, state } = server({ candidates: [candidate()] });
    api.on("POST /api/answer-bank/candidates/1/reject", () => {
      state.candidates = state.candidates.map((c) => ({ ...c, state: "rejected" }));
      return ok({ candidate: { ...candidate(), state: "rejected" }, entryId: "" });
    });
    api.on("POST /api/answer-bank/candidates/1/restore", () => {
      state.candidates = [candidate()];
      return ok({ candidate: candidate(), entryId: "" });
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => button("Reject")));
    await waitFor(() => text().includes("No suggested answers are waiting."));
    expect(button("Approve selected (0)")).toBeNull();
    expect(api.callsTo("POST /api/answer-bank/candidates/1/reject")).toHaveLength(1);
    expect(text()).toContain("Rejected: How did you hear about Example Corp?");
    await click(button("Undo"));
    await waitFor(() => button("Reject"));
  });

  it("approves one candidate without a confirmation and several only after one", async () => {
    const { api, state } = server({ candidates: [candidate(), candidate({ id: 2, key: "k2", seedQuestion: "Preferred start date?", questions: ["Preferred start date?"], proposedAnswer: "Two weeks" })] });
    api.on("POST /api/answer-bank/candidates/1/approve", () => {
      state.candidates = state.candidates.filter((c) => c.id !== 1);
      return ok({ candidate: { ...candidate(), state: "approved" }, entryId: "how-did-you-hear" });
    });
    api.on("POST /api/answer-bank/candidates/2/approve", () => {
      state.candidates = state.candidates.filter((c) => c.id !== 2);
      return ok({ candidate: { ...candidate({ id: 2 }), state: "approved" }, entryId: "start-date" });
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await waitFor(() => qa("li.candidate input[type=checkbox]").length === 2);
    await click(qa("li.candidate input[type=checkbox]")[0]);
    await click(qa("li.candidate input[type=checkbox]")[1]);
    expect(byText("label.check", "Select all")?.querySelector("input")?.checked).toBe(true);
    await click(button("Approve selected (2)"));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")).toHaveLength(0);
    expect(document.activeElement?.textContent).toBe("Cancel");
    await click(button("Approve all"));
    await waitFor(() => api.callsTo("POST /api/answer-bank/candidates/2/approve").length === 1);
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")).toHaveLength(1);
  });

  it("a bulk approval refused part way says how many it approved, and the button counts the picks still waiting", async () => {
    const more = (id: number, seedQuestion: string) => candidate({ id, key: `k${id}`, seedQuestion, questions: [seedQuestion] });
    const { api, state } = server({ candidates: [candidate(), more(2, "Preferred start date?"), more(3, "Notice period?")] });
    api.on("POST /api/answer-bank/candidates/1/approve", () => {
      state.candidates = state.candidates.filter((c) => c.id !== 1);
      return ok({ candidate: { ...candidate(), state: "approved" }, entryId: "how-did-you-hear" });
    });
    api.on("POST /api/answer-bank/candidates/2/approve", () => fail("conflict", "an entry already answers this question"));
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => byText("label.check", "Select all")?.querySelector("input")));
    await click(button("Approve selected (3)"));
    await confirm("Approve all");
    await waitFor(() => text().includes("an entry already answers this question"));
    expect(text()).toContain("Approved 1 of 3");
    expect(api.callsTo("POST /api/answer-bank/candidates/3/approve")).toHaveLength(0);
    const checked = qa<HTMLInputElement>("li.candidate input[type=checkbox]").filter((b) => b.checked).length;
    expect(checked).toBe(2);
    expect(button(/^Approve selected/)?.textContent).toBe(`Approve selected (${checked})`);
  });

  it("edits the proposed answer and approves with it in one press", async () => {
    const { api, state } = server({ candidates: [candidate()] });
    api.on("POST /api/answer-bank/candidates/1/approve", () => {
      state.candidates = [];
      return ok({ candidate: { ...candidate(), state: "approved" }, entryId: "how-did-you-hear" });
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => button("Edit and approve")));
    const box = q<HTMLInputElement>(".inline-form input")!;
    expect(document.activeElement).toBe(box);
    await type(box, "A friend");
    await submit(box.form);
    await waitFor(() => text().includes("Approved with your answer: How did you hear about Example Corp?"));
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")[0].body).toEqual({ answer: "A friend" });
    expect(api.callsTo("PUT /api/answer-bank")).toHaveLength(0);
  });

  it("Approve a candidate: Undo takes the approval back, and a take-back the server refuses says why", async () => {
    const { api, state } = server({ candidates: [candidate()] });
    api.on("POST /api/answer-bank/candidates/1/approve", () => {
      state.candidates = [{ ...candidate(), state: "approved" }];
      return ok({ candidate: state.candidates[0], entryId: "how-did-you-hear" });
    });
    let refuse = false;
    api.on("POST /api/answer-bank/candidates/1/unapprove", () => {
      if (refuse) return fail("conflict", "the entry it added was edited since, so the approval stays");
      state.candidates = [candidate()];
      return ok({ candidate: candidate(), entryId: "how-did-you-hear" });
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => button("Approve")));
    await waitFor(() => text().includes("Approved: How did you hear about Example Corp?"));
    expect(byText("[role=status] a", "Open the entry")?.getAttribute("href")).toBe("/answer-bank?entry=how-did-you-hear");
    await click(button("Undo"));
    await waitFor(() => text().includes("waits for your decision again; the entry it added is removed."));
    expect(api.callsTo("POST /api/answer-bank/candidates/1/unapprove")).toHaveLength(1);
    refuse = true;
    await click(await waitFor(() => button("Approve")));
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => text().includes("the entry it added was edited since, so the approval stays"));
    expect(button("Undo")).not.toBeNull();
  });

  it("leaves a value you changed by hand at its other press: that one edit is left, not kept, and the offer goes", async () => {
    let left = false;
    const edit = { fillId: 41, entryId: "home-city", host: "jobs.example.com", question: "Town you live in", before: "Durham", after: "Raleigh" };
    const { api } = server();
    api.on("GET /api/answer-bank/candidates", () => ok({ candidates: [], edits: left ? [] : [edit] }));
    api.on("POST /api/answer-bank/manual-fills/41/leave", () => {
      left = true;
      return ok(edit);
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => button("Keep Durham")));
    await waitFor(() => text().includes("Town you live in keeps its saved answer"));
    expect(api.callsTo("POST /api/answer-bank/manual-fills/41/leave")).toHaveLength(1);
    expect(api.callsTo("POST /api/answer-bank/manual-fills/41/keep")).toHaveLength(0);
    expect(text()).not.toContain("from Durham to Raleigh");
  });

  it("Undo takes back Use and Keep on a value you changed, and a take-back the server refuses says why", async () => {
    let offered = true;
    const edit = { fillId: 41, entryId: "home-city", host: "jobs.example.com", question: "Town you live in", before: "Durham", after: "Raleigh" };
    const { api } = server();
    api.on("GET /api/answer-bank/candidates", () => ok({ candidates: [], edits: offered ? [edit] : [] }));
    api.on("POST /api/answer-bank/manual-fills/41/keep", () => ((offered = false), ok({ revision: "rev-2", changed: true })));
    api.on("POST /api/answer-bank/manual-fills/41/leave", () => ((offered = false), ok(edit)));
    let refuse = false;
    api.on("POST /api/answer-bank/manual-fills/41/undo", () => (refuse ? fail("conflict", "the entry \"home-city\" changed after the press, so it keeps what it holds now") : ((offered = true), ok({ revision: "rev-3", changed: true }))));
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    await click(await waitFor(() => button("Use Raleigh")));
    await waitFor(() => text().includes("Town you live in is answered Raleigh from now on."));
    await click(button("Undo"));
    await waitFor(() => text().includes("Town you live in is answered Durham again"));
    await click(await waitFor(() => button("Keep Durham")));
    refuse = true;
    await click(await waitFor(() => button("Undo")));
    await waitFor(() => text().includes("changed after the press, so it keeps what it holds now"));
    expect(api.callsTo("POST /api/answer-bank/manual-fills/41/undo")).toHaveLength(2);
  });

  it("opens a reusable suggestion's draft in the answer box with its sources and approves what the box holds", async () => {
    const drafted = candidate({ proposedAnswer: "", review: { verdict: "reusable", reason: "One answer fits every application.", draft: "I write Go at work.", cited: ["knowledge/projects/reef.md"] } });
    const unsaid = candidate({ id: 2, key: "k2", seedQuestion: "Preferred editor?", proposedAnswer: "", review: { verdict: "reusable", reason: "Your profile and CV don't say.", draft: "", cited: [] } });
    const { api, state } = server({ candidates: [drafted, unsaid] });
    api.on("POST /api/answer-bank/candidates/1/approve", () => {
      state.candidates = [unsaid];
      return ok({ candidate: { ...drafted, state: "approved" }, entryId: "how-did-you-hear" });
    });
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    const box = await waitFor(() => q<HTMLInputElement>("li.candidate .inline-form input"));
    expect(box?.value).toBe("I write Go at work.");
    expect(box?.labels?.[0]?.textContent).toContain("Drafted from your profile and CV \u2014 check it before approving");
    expect(qa("li.panel")[0].textContent).toContain("Written from: your notes (reef).");
    expect(qa("li.panel")[1].textContent).toContain("Your profile and CV don't say \u2014 type your answer.");
    expect(qa("li.panel")[1].querySelector(".inline-form")).toBeNull();
    await type(box, "I write Go and Python at work.");
    await click(qa<HTMLButtonElement>("li.panel")[0].querySelector<HTMLButtonElement>("button.primary"));
    await waitFor(() => text().includes("Approved with your answer: How did you hear about Example Corp?"));
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")[0].body).toEqual({ answer: "I write Go and Python at work." });
  });

  it("a bulk approval sends each drafted box as it reads now, an edited one with the edit, and the dialog says how many are drafts", async () => {
    const draftOf = (id: number, seedQuestion: string, draft: string) =>
      candidate({ id, key: `k${id}`, seedQuestion, questions: [seedQuestion], proposedAnswer: "", review: { verdict: "reusable", reason: "One answer fits every application.", draft, cited: [] } });
    const { api, state } = server({ candidates: [draftOf(1, "Languages you speak?", "English"), draftOf(2, "Portfolio link?", "https://example.com/me"), candidate({ id: 3, key: "k3", seedQuestion: "Notice period?", questions: ["Notice period?"] })] });
    for (const id of [1, 2, 3]) {
      api.on(`POST /api/answer-bank/candidates/${id}/approve`, () => {
        state.candidates = state.candidates.filter((c) => c.id !== id);
        return ok({ candidate: { ...candidate({ id }), state: "approved" }, entryId: `e${id}` });
      });
    }
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    const boxes = await waitFor(() => (qa<HTMLInputElement>("li.candidate .inline-form input").length === 2 ? qa<HTMLInputElement>("li.candidate .inline-form input") : null));
    await type(boxes![0], "English and Spanish");
    await click(await waitFor(() => byText("label.check", "Select all")?.querySelector("input")));
    await click(button("Approve selected (3)"));
    expect((await waitFor(() => q("dialog")))?.textContent).toContain("3 new entries or phrasings are written to your answer bank, 2 with the answer drafted from your profile and CV as its box shows it.");
    await click(button("Approve all"));
    await waitFor(() => api.callsTo("POST /api/answer-bank/candidates/3/approve").length === 1);
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")[0].body).toEqual({ answer: "English and Spanish" });
    expect(api.callsTo("POST /api/answer-bank/candidates/2/approve")[0].body).toEqual({ answer: "https://example.com/me" });
    expect(api.callsTo("POST /api/answer-bank/candidates/3/approve")[0].body).toEqual({});
  });

  it("a bulk approval leaves out a drafted suggestion whose answer box was emptied, as its own Approve does", async () => {
    const draftOf = (id: number, seedQuestion: string, draft: string) =>
      candidate({ id, key: `k${id}`, seedQuestion, questions: [seedQuestion], proposedAnswer: "", review: { verdict: "reusable", reason: "One answer fits every application.", draft, cited: [] } });
    const { api, state } = server({ candidates: [draftOf(1, "Languages you speak?", "English"), draftOf(2, "Portfolio link?", "https://example.com/me"), candidate({ id: 3, key: "k3", seedQuestion: "Notice period?", questions: ["Notice period?"] })] });
    for (const id of [1, 2, 3]) {
      api.on(`POST /api/answer-bank/candidates/${id}/approve`, () => {
        state.candidates = state.candidates.filter((c) => c.id !== id);
        return ok({ candidate: { ...candidate({ id }), state: "approved" }, entryId: `e${id}` });
      });
    }
    await mount(<AnswerBank />);
    await openTab("Suggestions");
    const boxes = await waitFor(() => (qa<HTMLInputElement>("li.candidate .inline-form input").length === 2 ? qa<HTMLInputElement>("li.candidate .inline-form input") : null));
    await click(await waitFor(() => byText("label.check", "Select all")?.querySelector("input")));
    await type(boxes![0], " ");
    await click(button("Approve selected (2)"));
    expect((await waitFor(() => q("dialog")))?.textContent).toContain("2 new entries or phrasings are written to your answer bank, 1 with the answer drafted");
    await click(button("Approve all"));
    await waitFor(() => api.callsTo("POST /api/answer-bank/candidates/3/approve").length === 1);
    expect(api.callsTo("POST /api/answer-bank/candidates/1/approve")).toHaveLength(0);
    expect(api.callsTo("POST /api/answer-bank/candidates/2/approve")[0].body).toEqual({ answer: "https://example.com/me" });
    expect(text()).toContain("Approved 2 suggestions.");
  });

});

describe("Answer Bank: runtime settings", () => {
  it("shows the stored legal-declaration setting with a one-line scope and turns it on only after a confirmation", async () => {
    const { api, state } = server();
    await mount(<AnswerBank />);
    await openTab("Settings");
    await waitFor(() => text().includes("Legal declarations"));
    expect(byText(".chip", "Off")).not.toBeNull();
    const scope = text();
    expect(scope).toContain("required legal declarations");
    expect(scope).toContain("never optional marketing opt-ins");
    expect(scope).toContain("never EEO or work-authorization answers");
    expect(scope).toContain("never typed signatures");
    await click(button("Turn on"));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("PATCH /api/answer-bank/settings")).toHaveLength(0);
    await click(qa<HTMLButtonElement>("dialog button").find((b) => b.textContent === "Turn on")!);
    await waitFor(() => api.callsTo("PATCH /api/answer-bank/settings").length === 1);
    expect(api.callsTo("PATCH /api/answer-bank/settings")[0].body).toEqual({ autoAcceptLegalDeclarations: true });
    await waitFor(() => byText(".chip", "On"));
    expect(state.legal).toBe(true);
    await click(button("Turn off"));
    await waitFor(() => state.legal === false);
  });
});

describe("Answer Bank: transcript", () => {
  const pdf = (name: string, type = "application/pdf") => new File([new Uint8Array(20).fill(37)], name, { type });
  async function choose(file: File) {
    const picker = await waitFor(() => q<HTMLInputElement>("input[type=file]"));
    Object.defineProperty(picker, "files", { value: [file], configurable: true });
    await act(async () => {
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
  }
  // A transcript store that keeps the one file, as the server's does.
  function withTranscript(api: FakeApi, fileName = "") {
    const held = { fileName };
    api.on("GET /api/answer-bank/transcript", () => ok({ ...held }));
    api.on("POST /api/answer-bank/transcript", (c) => ((held.fileName = `${c.query.get("name")}.pdf`), ok({ ...held })));
    api.on("DELETE /api/answer-bank/transcript", () => ((held.fileName = ""), ok({ ...held })));
    return held;
  }

  it("uploads a PDF under its file name, shows that name, and a second upload replaces it; a file that is not a PDF is refused before sending", async () => {
    const { api } = server();
    withTranscript(api);
    await mount(<AnswerBank />);
    await openTab("Transcript");
    await waitFor(() => text().includes("No transcript yet."));
    await choose(pdf("notes.txt", "text/plain"));
    await waitFor(() => text().includes("Only PDF files can be uploaded."));
    expect(api.callsTo("POST /api/answer-bank/transcript")).toHaveLength(0);
    await choose(pdf("Unofficial Transcript.pdf"));
    await waitFor(() => byText(".resume-title strong", "unofficial-transcript.pdf"));
    expect(api.callsTo("POST /api/answer-bank/transcript")[0].headers.get("Content-Type")).toBe("application/pdf");
    expect(text()).toContain("Added unofficial-transcript.pdf.");
    await choose(pdf("Fall Term.pdf"));
    await waitFor(() => byText(".resume-title strong", "fall-term.pdf"));
    expect(text()).toContain("Replaced unofficial-transcript.pdf with fall-term.pdf.");
  });

  it("removes the transcript only after a confirmation", async () => {
    const { api } = server();
    const held = withTranscript(api, "unofficial-transcript.pdf");
    await mount(<AnswerBank />);
    await openTab("Transcript");
    await click(await waitFor(() => byLabel(/^Remove unofficial-transcript\.pdf$/)));
    await waitFor(() => q("dialog"));
    expect(api.callsTo("DELETE /api/answer-bank/transcript")).toHaveLength(0);
    await confirm("Remove");
    await waitFor(() => text().includes("No transcript yet."));
    expect(held.fileName).toBe("");
    expect(text()).toContain("Removed unofficial-transcript.pdf.");
  });
});

describe("Answer Bank: live", () => {

  it("shows a change made elsewhere when the answerbank topic moves, and keeps an unsaved edit through it", async () => {
    const city = (answer: string) => entry({ id: "city", description: "City", category: "contact", answer });
    const { state, elsewhere } = server({ entries: [entry(), city("Durham")] });
    const board = manualBoard();
    const set = (answerbank: number) => act(async () => board.set({ answerbank }));
    await mount(
      <BoardContext.Provider value={board}>
        <AnswerBank />
      </BoardContext.Provider>,
    );
    await set(1);
    await openEntry("City");
    const answer = () => qa<HTMLTextAreaElement>("dialog.drawer textarea").find((t) => t.labels?.[0]?.textContent === "Answer")!;
    elsewhere(() => (state.entries = [entry({ answer: "No" }), city("Raleigh")]));
    await set(2);
    // An open entry with no edit of its own takes the new answer.
    await waitFor(() => answer().value === "Raleigh");
    await type(answer(), "Cary");
    elsewhere(() => (state.entries = [entry({ answer: "Maybe" }), city("Apex")]));
    await set(3);
    await waitFor(() => qa(".entry-preview").some((p) => p.textContent === "Maybe"));
    expect(answer().value).toBe("Cary");
  });
});
