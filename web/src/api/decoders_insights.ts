// Decoders for the outcome cohorts of the Tracker and the skill-gap analyses of
// Reports. Each mirrors a struct of internal/api/wire.
import type {
  Gap, GapExclusion, GapInferred, GapInput, GapList, GapLookup, GapPlan, GapPlanItem, GapReport, GapResource, GapSource, GapSummary,
  OutcomeGroup, OutcomeStageReach, TrackerOutcomes,
} from "../wire";
import { bool, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const stageReach: Decoder<OutcomeStageReach> = object<OutcomeStageReach>({ stage: str, numerator: safeInt, denominator: safeInt, open: safeInt, unknown: safeInt });

const outcomeGroup: Decoder<OutcomeGroup> = object<OutcomeGroup>({
  key: str, applications: safeInt, known: safeInt, unknown: safeInt, open: safeInt, closed: safeInt, stages: orEmpty(stageReach), insufficient: orEmpty(str),
});

export const trackerOutcomes: Decoder<TrackerOutcomes> = object<TrackerOutcomes>({
  from: str, to: str, asOf: str, overall: outcomeGroup, bySource: orEmpty(outcomeGroup), byRoleFamily: orEmpty(outcomeGroup), folded: safeInt, noApplicationDate: safeInt,
});

const gapSource: Decoder<GapSource> = object<GapSource>({
  report: safeInt, company: str, role: str, requirement: str, quote: str, class: str, evidence: str, unsupported: bool,
});
const gap: Decoder<Gap> = object<Gap>({ skill: str, label: str, state: str, required: safeInt, preferred: safeInt, sources: orEmpty(gapSource) });
const inferred: Decoder<GapInferred> = object<GapInferred>({ report: safeInt, company: str, role: str, label: str, evidence: str });
const exclusion: Decoder<GapExclusion> = object<GapExclusion>({ report: safeInt, reason: str, duplicateOf: safeInt });
const input: Decoder<GapInput> = object<GapInput>({ report: safeInt, createdAt: time, contentHash: str });
const resource: Decoder<GapResource> = object<GapResource>({ title: str, url: str });
const planItem: Decoder<GapPlanItem> = object<GapPlanItem>({
  skill: str, label: str, exercise: str, criterion: str, effort: str, resources: orEmpty(resource), done: bool,
});
const lookup: Decoder<GapLookup> = object<GapLookup>({ status: str, reason: str });
const plan: Decoder<GapPlan> = object<GapPlan>({ generatedAt: time, items: orEmpty(planItem), lookup });

export const gapReport: Decoder<GapReport> = object<GapReport>(
  {
    id: str, generatedAt: time, selected: orEmpty(safeInt), sample: orEmpty(input), excluded: orEmpty(exclusion), gaps: orEmpty(gap),
    inferred: orEmpty(inferred), done: orEmpty(str),
  },
  { plan },
  ["plan"],
);

const summary: Decoder<GapSummary> = object<GapSummary>({ id: str, generatedAt: time, sample: safeInt, gaps: safeInt });
export const gapList: Decoder<GapList> = object<GapList>({ analyses: orEmpty(summary) });
