// Which tab a panel opens on when nothing was remembered: on an application page, the Autofill tab.
import { formControlCount, isSupportedSite } from "../shared/site";

/** Application systems the panel recognises by host alone, beside the engine's site table (isSupportedSite), which the panel auto-opens on; this list adds hosts that table does not name as one (old Greenhouse boards, Oracle Cloud, Paycor, Phenom). */
const SYSTEM_HOST =
  /(^|\.)(successfactors|sapsf|ns2cloud|greenhouse|lever|ashbyhq|icims|myworkdayjobs|myworkdaysite|oraclecloud|paycor|smartrecruiters|workable|phenom\w*|jobvite|taleo|dayforcehcm|ultipro|ukg|paylocity|bamboohr)\./i;
/** A form, or the document as a whole, with at least this many fillable controls is an application worth opening on. */
const MIN_FILLABLE = 2;

export function defaultTab(doc: Document, url: string): "autofill" | undefined {
  let host = "";
  try {
    host = new URL(url).hostname;
  } catch {
    /* no host: only the form decides */
  }
  return isSupportedSite(url) || SYSTEM_HOST.test(`${host}.`) || formControlCount(doc) >= MIN_FILLABLE ? "autofill" : undefined;
}
