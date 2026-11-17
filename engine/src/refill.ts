// Clean-room: docs/provenance.md#CR-ENGINE
// A page that re-renders under the fill. Three repairs, each bounded: the
// whole fill pass again when the block the profile pass wrote into was swapped
// (a résumé parse), the identity refill for name, email, phone, link and
// address boxes that read empty again, and the tail guard that keeps looking
// for a late remount after the summary went out.

import type { WriteCtx } from "./env";
import type { ProfileItem, ProfileKey } from "./profile";
import { planProfile, recognise } from "./profile";
import type { ApplicantSnapshot } from "./protocol";
import type { Run, RunLog } from "./run";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import type { UserEdits } from "./restore";
import { profileEnv, writeProfileValue } from "./stage-profile";

/** Extra whole fill passes after a form was replaced, and identity refill passes. */
const MAX_EXTRA_PASSES = 2;
const MAX_IDENTITY_PASSES = 2;
/** At least this many writes must be lost before a form counts as replaced. */
const REPLACED_MIN = 3;
/** Consecutive quiet looks after a repair before the tail guard stops early. */
const TAIL_STABLE_LOOKS = 3;
/** The most the tail guard writes one control. */
const TAIL_MAX_WRITES = 3;

/**
 * Name, email, phone, link and address text boxes, refilled wherever they are text. State and country are left
 * out of the set: wipedIdentity puts a state back only on a plain control, and a country only when it would wipe
 * no dependent.
 */
export const IDENTITY_KEYS: ReadonlySet<ProfileKey> = new Set<ProfileKey>([
  "fullName",
  "firstName",
  "lastName",
  "email",
  "confirmEmail",
  "phone",
  "linkedin",
  "github",
  "portfolio",
  "addressLine1",
  "addressLine2",
  "city",
  "county",
  "postalCode",
]);

/** The boxes a country's commit may blank. */
const DEPENDENTS: ReadonlySet<ProfileKey | null> = new Set<ProfileKey>(["state", "city", "postalCode"]);

interface Wiped {
  doc: Document;
  /** A scan of the page as it is now, when the caller has one. */
  infos?: ControlInfo[];
  applicant: ApplicantSnapshot;
  claimed: Set<string>;
  written: Map<string, string>;
  edits: UserEdits;
}

/** Identity boxes this run wrote that read empty now, never one the user touched; a committed listbox value is not empty. */
function wipedIdentity(w: Wiped): ProfileItem[] {
  const infos = w.infos ?? scanControls(w.doc);
  let empty: boolean | undefined;
  // Read only when a country was blanked: every state, city and postal code box of the page is empty.
  const dependentsEmpty = () => (empty ??= infos.filter((i) => DEPENDENTS.has(recognise(i))).every((i) => readInfo(i) === ""));
  return planProfile(infos, w.applicant, w.claimed).filter((i) => {
    if (i.action !== "write" || !w.written.has(i.info.selector) || w.edits.edited(i.info)) return false;
    if (IDENTITY_KEYS.has(i.key as ProfileKey) && i.info.kind === "text") return true;
    // A state or a country is put back only on a plain control, and a country only when it would wipe no dependent.
    const plain = (i.info.kind === "text" || i.info.kind === "select") && i.info.driver === null;
    return plain && (i.key === "state" || (i.key === "country" && dependentsEmpty()));
  });
}

/**
 * The selectors of the profile writes the page took away, or null when the
 * form still looks like the one that was written: at least 3 and at least half
 * of them gone or hidden, or at least 3 text writes reading empty again.
 */
function lostProfileWrites(run: Run, edits: UserEdits): string[] | null {
  const infos = new Map(scanControls(run.doc).map((i) => [i.selector, i]));
  const writes = Array.from(run.rows.values()).filter((r) => r.lane === "profile" && r.outcome === "filled" && r.kind !== "file");
  const gone = writes.filter((r) => !infos.has(r.selector));
  const empty = writes.filter((r) => {
    const info = infos.get(r.selector);
    return !!info && r.kind !== "radio-group" && r.kind !== "checkbox" && readInfo(info) === "" && !edits.edited(info);
  });
  const replaced = (gone.length >= REPLACED_MIN && gone.length * 2 >= writes.length) || empty.length >= REPLACED_MIN;
  return replaced ? [...gone, ...empty].map((r) => r.selector) : null;
}

export interface RefillDeps {
  written: Map<string, string>;
  edits: UserEdits;
  /** One more fill pass: questions, profile, apply loop, cascade. The résumé is never attached again. */
  pass: () => Promise<void>;
}

/**
 * Runs after the first fill pass. A replaced form gets up to two more whole
 * passes; then up to two identity passes put back what still reads empty.
 * Nothing runs when the profile pass wrote nothing.
 */
export async function refillStage(run: Run, d: RefillDeps): Promise<void> {
  const wrote = Array.from(run.rows.values()).some((r) => r.lane === "profile" && r.outcome === "filled");
  if (!wrote) return;
  const settle = run.timing.refillSettleMs;
  await run.sleep(settle);
  for (let extra = 1; extra <= MAX_EXTRA_PASSES; extra++) {
    const lost = lostProfileWrites(run, d.edits);
    if (!lost) break;
    run.log.add("refill", "form-replaced", { extra, lost: lost.length });
    for (const selector of lost) {
      run.rows.delete(selector);
      d.written.delete(selector);
    }
    await d.pass();
    await run.sleep(settle);
  }
  for (let pass = 1; pass <= MAX_IDENTITY_PASSES && run.applicant; pass++) {
    const items = wipedIdentity({ doc: run.doc, applicant: run.applicant, claimed: run.claimed, written: d.written, edits: d.edits });
    if (!items.length) break;
    for (const item of items) {
      await run.checkpoint();
      const row = await writeProfileValue(profileEnv(run), item, d.written);
      if (row.outcome === "filled") run.setRow(row);
    }
    run.log.add("refill", "identity", { pass, boxes: items.length });
    await run.sleep(settle);
  }
}

export interface TailEnv {
  doc: Document;
  log: RunLog;
  edits: UserEdits;
  written: Map<string, string>;
  claimed: Set<string>;
  applicant: ApplicantSnapshot;
  acceptLegalDeclarations: boolean;
  capMs: number;
}

/**
 * Looks for identity boxes a late remount blanked after the summary went out.
 * It runs inside the restore loop, so a look and a restore never write at the
 * same time. It watches for the cap, and stops early only after a repair
 * followed by three quiet looks: a remount that has not come yet is exactly
 * what it is waiting for. It lets go of the applicant data when it ends.
 */
export class TailGuard {
  private applicant: ApplicantSnapshot | null;
  private began = 0;
  private quiet = 0;
  private repaired = false;
  private readonly writes = new Map<string, number>();

  constructor(private readonly env: TailEnv) {
    this.applicant = env.applicant;
  }

  get finished(): boolean {
    return this.applicant === null;
  }

  begin(): void {
    this.began = Date.now();
  }

  /** Ends the watch and drops the applicant data. */
  release(reason = "stopped"): void {
    if (this.applicant === null) return;
    this.applicant = null;
    this.env.log.add("tail", "end", { reason });
  }

  /** One look; `fixed` is told each selector that was written back. `infos` is the caller's scan of the page as it is now, when it has one. */
  async step(ctx: WriteCtx, fixed: (selector: string) => void, infos?: ControlInfo[]): Promise<void> {
    const applicant = this.applicant;
    if (!applicant) return;
    if (Date.now() - this.began >= this.env.capMs) {
      this.release("cap");
      return;
    }
    const e = this.env;
    const items = wipedIdentity({ doc: e.doc, infos, applicant, claimed: e.claimed, written: e.written, edits: e.edits }).filter((i) => (this.writes.get(i.info.selector) ?? 0) < TAIL_MAX_WRITES);
    if (!items.length) {
      this.quiet = this.repaired ? this.quiet + 1 : 0;
      if (this.quiet >= TAIL_STABLE_LOOKS) this.release("stable");
      return;
    }
    this.repaired = true;
    this.quiet = 0;
    for (const item of items) {
      await ctx.checkpoint();
      this.writes.set(item.info.selector, (this.writes.get(item.info.selector) ?? 0) + 1);
      const row = await writeProfileValue({ ctx, applicant, acceptLegalDeclarations: e.acceptLegalDeclarations, edits: e.edits, log: e.log }, item, e.written);
      if (row.outcome === "filled") fixed(item.info.selector);
    }
  }
}
