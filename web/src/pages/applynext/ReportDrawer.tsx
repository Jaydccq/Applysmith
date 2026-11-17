import { useEffect, type ReactNode } from "react";
import { get } from "../../api/client";
import { reportDetail } from "../../api/decoders";
import { useResource } from "../../hooks";
import { Link } from "../../router";
import { Markdown } from "../../ui/Markdown";
import { Modal } from "../../ui/Modal";
import { Chip, ErrorNote, ExternalLink, ageLabel, formatDateTime, formatScore, humanize, Loading } from "../../ui/parts";
import type { ReportSummary } from "../../wire";
import { payOf, reportBody } from "../reports/body";

// verificationWords says how the posting was read ("{method} {result}
// {date}", internal/evaluate): live only when a browser saw it open.
function verificationWords(v: string): { label: string; tone: "good" | "neutral" } {
  const [method, result, date] = v.split(" ");
  if (result === "live") return { label: `Posting live${date ? ` on ${date}` : ""}`, tone: "good" };
  return { label: method && result ? `Not checked live (${method})` : humanize(v || "unverified"), tone: "neutral" };
}

// ReportChips is a report's verdict in chips, the same in the drawer and on
// the Reports page: score, recommendation, the stated pay, how the posting was
// read, legitimacy, and how old the report is.
export function ReportChips({ report, content }: { report: ReportSummary; content: string }) {
  const v = verificationWords(report.verification);
  const pay = payOf(content);
  const legit = report.legitimacy === "credible" ? "good" : report.legitimacy === "doubtful" ? "bad" : "neutral";
  return (
    <p className="chips">
      <Chip>Score {formatScore(report.score)}</Chip> <Chip>{humanize(report.recommendation)}</Chip>{" "}
      <Chip tone={v.tone}>
        <span title={report.verification}>{v.label}</span>
      </Chip>{" "}
      {pay && <Chip>{`Pay ${pay}`}</Chip>} <Chip tone={legit}>Legitimacy {report.legitimacy}</Chip>{" "}
      <Chip>
        <span title={formatDateTime(report.createdAt)}>Evaluated {ageLabel(report.createdAt)}</span>
      </Chip>
    </p>
  );
}

// ReportDrawer reads one evaluation report without leaving the list, and ends
// on the row's own actions so reading it leads straight to acting on it. The
// text goes through the Markdown renderer, which never sets it as HTML.
// A list that knows its reports in order passes `reports` (their numbers) and
// `onStep`, so the next one opens from here without closing this one first.
export function ReportDrawer({ number, actions, onClose, reports, onStep }: { number: number; actions?: ReactNode; onClose: () => void; reports?: number[]; onStep?: (number: number) => void }) {
  const [res, retry] = useResource(() => get(`/api/reports/${number}`, reportDetail), [number], ["reports"]);
  // A report stepped to opens at its start.
  useEffect(() => {
    document.querySelector("dialog[open] .modal-body")?.scrollTo?.({ top: 0 });
  }, [number]);
  const at = reports?.indexOf(number) ?? -1;
  // A step keeps the report before it until the next one has loaded, and
  // after its load failed: that one is not drawn under this number. The steps
  // stay, so the keyboard keeps its place on them.
  const data = res.data?.report.number === number ? res.data : undefined;
  return (
    <Modal title={`Report ${number}`} variant="drawer" onClose={onClose}>
      {(res.status === "loading" || (res.status === "ready" && !data)) && <Loading />}
      {res.status === "error" && !data && <ErrorNote error={res.error} onRetry={retry} />}
      {res.data && (
        <article aria-label={data && `${data.report.company}: ${data.report.role}`} className="viewer">
          {data && (
            <>
              <h3>
                {data.report.company}: {data.report.role}
              </h3>
              <ReportChips report={data.report} content={data.content} />
            </>
          )}
          <p className="actions">
            {reports && onStep && at >= 0 && reports.length > 1 && (
              <>
                <button type="button" className="small" disabled={at === 0} aria-label="Previous report" onClick={() => onStep(reports[at - 1])}>
                  Previous
                </button>
                <span className="muted" aria-live="polite">
                  {at + 1} of {reports.length}
                </span>
                <button type="button" className="small" disabled={at === reports.length - 1} aria-label="Next report" onClick={() => onStep(reports[at + 1])}>
                  Next
                </button>
              </>
            )}
            <Link to={`/reports?id=${number}`}>Open in Reports</Link>
            {data?.report.postingUrl && <ExternalLink href={data.report.postingUrl}>Open the posting</ExternalLink>}
          </p>
          {data && <Markdown source={reportBody(data.content)} />}
        </article>
      )}
      <div className={actions ? "actions foot" : "actions"}>
        {actions}
        <button type="button" onClick={onClose}>
          Close
        </button>
      </div>
    </Modal>
  );
}
