import { get } from "../../api/client";
import { usageDays } from "../../api/decoders_agent";
import { useResource } from "../../hooks";
import { Panel, usd } from "../../ui/parts";
import type { UsageDay } from "../../wire";
import { CostChart } from "./Chart";

const DAYS = 14;

function DayTable({ days }: { days: UsageDay[] }) {
  return (
    <table className="cards">
      <caption className="sr-only">Cost by day</caption>
      <thead>
        <tr>
          <th scope="col" className="nowrap">Day</th>
          <th scope="col" className="num">Calls</th>
          <th scope="col" className="num">Cost</th>
        </tr>
      </thead>
      <tbody>
        {days.map((d) => (
          <tr key={d.day}>
            <td data-label="Day" className="nowrap">{d.day}</td>
            <td data-label="Calls" className="num">{d.calls}</td>
            <td data-label="Cost" className="num">{usd(d.costUsd)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function CostByDay() {
  const [res, reload] = useResource(() => get("/api/llm/cost/daily", usageDays, { days: DAYS }), [], ["runs"]);
  return (
    <Panel title={`Cost by day, last ${DAYS} days`} id="usage-days" res={res} retry={reload}>
      {({ days }) => <CostChart caption="Cost by day" empty={`No model call in the last ${DAYS} days.`} items={days.some((d) => d.calls > 0) ? days.map((d) => ({ key: d.day, label: d.day, costUsd: d.costUsd })) : []} table={<DayTable days={days} />} />}
    </Panel>
  );
}
