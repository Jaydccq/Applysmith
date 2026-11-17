// Clean-room: docs/provenance.md#CR-ENGINE
// Profile-field pass and its apply loop.

import { makeRow } from "./answers";
import { applyValue } from "./apply";
import { dependencyOrder } from "./cascade";
import { itiRoot, writeItiCountry } from "./libraries";
import { commitPhoneCountry, countryOfPhone, findPhoneCountryPicker } from "./phone-country";
import { agrees, planProfile, type ProfileItem, type ProfileKey } from "./profile";
import { isParserHost, planRows } from "./row-plan";
import { isOracleCountry, isOraclePostalLead } from "./oracle";
import { waitFor } from "./env";
import type { WriteCtx } from "./env";
import type { ApplicantSnapshot, Row } from "./protocol";
import type { QuietWatcher } from "./quiet";
import type { UserEdits } from "./restore";
import type { Run, RunLog } from "./run";
import { readInfo, scanControls } from "./scan";

export interface ProfileDeps {
  written: Map<string, string>;
  quiet: QuietWatcher;
}

/** Bind identity and contact controls the question pass did not claim to profile fields; a country comes before its dependents. */
export function profilePass(run: Run): ProfileItem[] {
  if (!run.applicant) return [];
  const infos = scanControls(run.doc);
  const own = dependencyOrder(planProfile(infos, run.applicant, run.claimed, run.site), (i) => i.info.label.text);
  return [...own, ...planRows(infos, run.applicant, run.claimed, run.site, run.doc)];
}

/** What writing one planned profile value needs; the tail guard supplies it after the run is over. */
export interface ProfileEnv {
  ctx: WriteCtx;
  applicant: ApplicantSnapshot | null;
  acceptLegalDeclarations: boolean;
  edits: UserEdits;
  log: RunLog;
}

/** Write one planned value and report the row. A phone's unlabeled country-code picker is set first. */
export async function writeProfileValue(env: ProfileEnv, item: ProfileItem, written: Map<string, string>, deadline?: number): Promise<Row> {
  const { info } = item;
  const { ctx } = env;
  const addr = env.applicant?.candidate.address;
  // The country picker is part of the phone box: a box the user owns keeps its picker, and so does a picker the user set.
  if (item.key === "phone" && info.el.localName === "input" && !env.edits.edited(info)) {
    const dial = env.applicant?.candidate.phoneCountryCode ?? "";
    const country = countryOfPhone(dial, addr?.country);
    const iti = itiRoot(info.el);
    const picker = iti ? null : findPhoneCountryPicker(info.el as HTMLInputElement);
    const control = iti ?? picker;
    if (country && control && !env.edits.editedWithin(control)) {
      const out = iti ? await writeItiCountry(iti, country, ctx, dial) : await commitPhoneCountry(picker as HTMLInputElement, country, ctx, dial);
      env.log.add("phone-country", out.ok ? "committed" : "left", out.ok ? {} : { reason: out.reason });
    }
  }
  const r = await applyValue(
    info,
    {
      value: item.value ?? "",
      replaceDefault: item.overwrite === true,
      currentDate: item.key === "signatureDate",
      phone: item.key === "phone",
      address: item.key === "addressLine1" || (item.key === "postalCode" && isOraclePostalLead(info.el)) ? { city: addr?.city, state: addr?.state, postalCode: addr?.postalCode, county: addr?.county } : undefined,
      place: item.key === "city" ? { state: addr?.state, country: addr?.country } : undefined,
      degree: item.degree,
    },
    { ctx, written, mode: item.overwrite ? "replace" : "keep", acceptLegalDeclarations: env.acceptLegalDeclarations, edits: env.edits, deadline },
  );
  return makeRow(info, "profile", r.outcome, { reason: r.reason, value: r.value, optionLabel: r.optionLabel, detail: r.detail });
}

export function profileEnv(run: Run): ProfileEnv {
  return { ctx: run.ctx(), applicant: run.applicant, acceptLegalDeclarations: run.acceptLegalDeclarations, edits: run.edits, log: run.log };
}

export function writeProfileItem(run: Run, item: ProfileItem, d: ProfileDeps, deadline?: number): Promise<Row> {
  return writeProfileValue(profileEnv(run), item, d.written, deadline);
}

/** Write the planned values, in document order, within the apply budget. */
export async function applyLoop(run: Run, plan: ProfileItem[], d: ProfileDeps): Promise<void> {
  const began = run.active();
  d.quiet.start();
  const queue = plan.slice();
  const seen = new Set<string>();
  let over = false;
  for (let at = 0; at < queue.length; at++) {
    const item = queue[at] as ProfileItem;
    await run.checkpoint();
    const { info } = item;
    seen.add(info.selector);
    if (item.action === "ask-me") {
      run.setRow(makeRow(info, "profile", "skipped", { reason: "ask-me" }));
      continue;
    }
    if (item.action === "no-data") continue;
    if (item.action === "prefilled") {
      // A value this run wrote through another lane stays that lane's row.
      if (d.written.has(info.selector)) continue;
      const authored = run.emptyAtStart.has(info.selector);
      run.setRow(
        makeRow(info, "profile", "prefilled", {
          value: readInfo(info),
          reason: authored ? "page-authored-value" : undefined,
          review: authored,
        }),
      );
      continue;
    }
    const deadline = began + run.profileApplyBudgetMs;
    if (run.active() > deadline) {
      if (!over) run.log.add("apply-loop", "budget-exhausted", { budgetMs: run.profileApplyBudgetMs, left: queue.length - at });
      over = true;
      run.setRow(makeRow(info, "profile", "skipped", { reason: "timeout" }));
      continue;
    }
    let row = await writeProfileItem(run, item, d, deadline);
    // A box that was empty when the run began and holds a value the run did not write was filled by the page itself, mid-run.
    if (row.outcome === "prefilled" && row.reason === "existing-value" && run.emptyAtStart.has(info.selector) && !d.written.has(info.selector)) {
      // Where the page's own résumé parser guesses, the applicant's value wins over a different guess here too (a box a person edited is skipped by the writer).
      const shown = readInfo(info);
      if (isParserHost(run.site) && !item.key.startsWith("row:") && item.key !== "signatureDate" && !!item.value && !agrees(item.key as ProfileKey, shown, item.value)) {
        row = await writeProfileItem(run, { ...item, overwrite: true }, d, deadline);
      }
      if (row.outcome === "prefilled" && row.reason === "existing-value") {
        row.reason = "page-authored-value";
        row.review = true;
      }
    }
    run.setRow(row);
    // A row's country decides the shape of its address block: the page needs a moment to morph it.
    // Oracle renders the address block only once the country has committed: the master is waited for.
    if (item.key === "country" && isOracleCountry(info.el) && run.applicant?.candidate.address?.line1) {
      const appeared = await waitFor(run.ctx(), () => run.doc.querySelector('[name="addressLine1"]'), run.timing.oracleGateWaitMs);
      if (appeared) queue.splice(at + 1, queue.length, ...profilePass(run).filter((i) => !seen.has(i.info.selector)));
    }
    if (item.key === "row:country" || item.key === "row:graduated") {
      await run.sleep(run.timing.rowCountryMs);
      // The block now has the shape its country gives it: what is left is planned again against it.
      queue.splice(at + 1, queue.length, ...profilePass(run).filter((i) => !seen.has(i.info.selector)));
    }
    await run.sleep(info.kind === "custom-listbox" ? run.timing.listboxSettleMs : run.timing.settleMs);
    await d.quiet.settle(run);
  }
}
