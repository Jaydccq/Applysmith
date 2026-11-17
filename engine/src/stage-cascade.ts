// Clean-room: docs/provenance.md#CR-ENGINE
// The cascade-completion pass: controls that only became fillable after an
// earlier write (a state list that stayed disabled until the country was set)
// are picked up by one extra profile pass.

import { answerControl } from "./answers";
import type { ProfileItem } from "./profile";
import type { QuietWatcher } from "./quiet";
import type { Run } from "./run";
import { readInfo, scanControls, type ControlInfo } from "./scan";
import { profilePass, writeProfileItem } from "./stage-profile";

/** At most this many revealed controls are written by the cascade pass. */
const MAX_CASCADE_WRITES = 8;

export interface CascadeDeps {
  written: Map<string, string>;
  quiet: QuietWatcher;
}

/**
 * Skips selectors already written and controls with a value, writes at most
 * eight, a short pause apart, and retries once an enabled combobox that
 * opened with no options the first time (an unsatisfied dependency).
 */
export async function cascadeCompletion(run: Run, d: CascadeDeps): Promise<void> {
  let wrote = 0;
  if (run.applicant) {
    const items: ProfileItem[] = profilePass(run).filter(
      // A field the apply budget left to the person stays theirs: only controls revealed since are picked up.
      (i) => i.action === "write" && !d.written.has(i.info.selector) && run.rows.get(i.info.selector)?.reason !== "timeout" && readInfo(i.info) === "",
    );
    for (const item of items) {
      if (wrote >= MAX_CASCADE_WRITES) break;
      await run.checkpoint();
      run.setRow(await writeProfileItem(run, item, d));
      wrote++;
      await run.sleep(run.timing.cascadeGapMs);
    }
  }
  const retries = Array.from(run.unsatisfied.entries());
  run.unsatisfied.clear();
  // The page is read again only after a write: until one lands it is as the last scan read it.
  let infos: ControlInfo[] | null = null;
  for (const [selector, res] of retries) {
    if (wrote >= MAX_CASCADE_WRITES) break;
    await run.checkpoint();
    infos ??= scanControls(run.doc);
    const info = infos.find((i) => i.selector === selector);
    if (!info || readInfo(info) !== "") continue;
    run.setRow(await answerControl(run, info, res, d.written));
    infos = null;
    wrote++;
    await run.sleep(run.timing.cascadeGapMs);
  }
  if (wrote) run.log.add("cascade", "pass", { wrote });
}
