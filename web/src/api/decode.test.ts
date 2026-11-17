// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  WireError,
  boardNotice,
  boardSnapshot,
  bool,
  decode,
  failure,
  health,
  object,
  safeInt,
  streamReset,
  success,
  time,
  type Decoder,
} from "./decode";
import type { Failure, Health, Success } from "../wire";
import { CodeRateLimited, CodeUnauthenticated } from "../wire";
import { evaluationView } from "./decoders_discovery";
import { applicationLookup } from "./decoders";

it("decodes completed evaluations with their score and recommendation", () => {
  const result = { runId: "run-1", status: "succeeded", review: false, outcome: "reported", reportNumber: 1, score: 4.5, recommendation: "APPLY" };
  expect(decode(JSON.stringify(result), evaluationView)).toEqual(result);
  expect(() => decode(JSON.stringify({ ...result, score: "4.5" }), evaluationView)).toThrow(WireError);
  expect(decode(JSON.stringify({ runId: "pending", status: "queued", review: false }), evaluationView).score).toBeUndefined();
});

it("decodes an application lookup with the posting's submitted ledger row", () => {
  const submission = { confirmed: true, source: "apply-chain", at: "2026-01-02T03:04:05.000Z" };
  expect(decode(JSON.stringify({ found: false, identityKey: "example", submission }), applicationLookup).submission).toEqual(submission);
  expect(() => decode(JSON.stringify({ found: false, submission: { ...submission, extra: 1 } }), applicationLookup)).toThrow(WireError);
});

// The same synthetic documents the Go tests of internal/api/wire decode.
const files = import.meta.glob("../../../testdata/wire/*.json", { query: "?raw", import: "default", eager: true }) as Record<string, string>;

function fixture(name: string): string {
  const text = files[`../../../testdata/wire/${name}`];
  if (text === undefined) throw new Error(`fixture ${name} is missing`);
  return text;
}

// The probe is a test-only shape that exercises each scalar rule.
interface ContractProbe {
  answer?: boolean | null;
  count: number;
  at: string;
}

const probe: Decoder<ContractProbe> = object<ContractProbe>({ count: safeInt, at: time }, { answer: bool }, ["answer"]);

const decoders: Record<string, Decoder<unknown>> = {
  Failure: failure,
  SuccessHealth: success(health),
  ContractProbe: probe,
  BoardSnapshot: boardSnapshot,
  BoardNotice: boardNotice,
  StreamReset: streamReset,
};

interface Entry {
  file: string;
  type: string;
  valid: boolean;
  canonical?: string;
}

const manifest = JSON.parse(fixture("manifest.json")) as Entry[];

describe("the shared wire fixtures", () => {
  it("lists every fixture file", () => {
    const listed = new Set(manifest.map((e) => e.file));
    for (const path of Object.keys(files)) {
      const name = path.split("/").pop()!;
      if (name === "manifest.json" || name.endsWith(".canonical.json")) continue;
      expect(listed.has(name), `${name} is not in the manifest`).toBe(true);
    }
  });

  for (const entry of manifest) {
    it(`${entry.file} is ${entry.valid ? "accepted" : "refused"}`, () => {
      const dec = decoders[entry.type];
      expect(dec, `no decoder for ${entry.type}`).toBeDefined();
      const text = fixture(entry.file);
      if (!entry.valid) {
        expect(() => decode(text, dec)).toThrow(WireError);
        return;
      }
      const value = decode(text, dec);
      const want = JSON.parse(fixture(entry.canonical ?? entry.file));
      expect(value).toEqual(want);
    });
  }
});

describe("absent, null and false stay apart", () => {
  const doc = (answer?: string) => `{${answer === undefined ? "" : `"answer":${answer},`}"count":0,"at":"2026-01-02T03:04:05.000Z"}`;
  const answer = (raw?: string) => decode(doc(raw), probe).answer;

  it("tells the four states apart", () => {
    expect(answer()).toBeUndefined();
    expect(answer("null")).toBeNull();
    expect(answer("false")).toBe(false);
    expect(answer("true")).toBe(true);
  });

  it("keeps the key of an explicit null", () => {
    expect("answer" in decode(doc("null"), probe)).toBe(true);
    expect("answer" in decode(doc(), probe)).toBe(false);
  });
});

describe("timestamps", () => {
  it("normalizes an offset to UTC milliseconds", () => {
    expect(decode(fixture("probe-offset-time.json"), probe).at).toBe("2026-01-02T03:04:05.000Z");
  });

  it("refuses a local time and a date without a time", () => {
    expect(() => time("2026-01-02T03:04:05", "$.at")).toThrow(WireError);
    expect(() => time("2026-01-02", "$.at")).toThrow(WireError);
    expect(() => time("2026-13-45T00:00:00Z", "$.at")).toThrow(WireError);
  });
});

describe("integers", () => {
  it("holds both ends of the safe range", () => {
    expect(decode(fixture("probe-max-safe-int.json"), probe).count).toBe(Number.MAX_SAFE_INTEGER);
    expect(decode(fixture("probe-negative-max-safe-int.json"), probe).count).toBe(-Number.MAX_SAFE_INTEGER);
  });

  it("refuses what JSON.parse would round, truncate or reinterpret", () => {
    expect(() => decode(fixture("probe-unsafe-int.json"), probe)).toThrow(/safe integer range/);
    expect(() => decode(fixture("probe-fractional-int.json"), probe)).toThrow(/safe integer/);
    expect(() => decode(fixture("probe-exponent-int.json"), probe)).toThrow(/exponent/);
  });

  it("does not mistake digits inside a string for a number", () => {
    expect(decode('{"reason":"id 99999999999999999999 and 1e5"}', streamReset).reason).toContain("99999999999999999999");
  });
});

describe("envelopes", () => {
  it("decodes a failure with its details", () => {
    const f: Failure = decode(fixture("failure-rate-limited.json"), failure);
    expect(f.error.code).toBe(CodeRateLimited);
    expect(f.error.retryable).toBe(true);
    expect(f.error.details).toEqual({ retryAfterSeconds: 12 });
    expect(decode(fixture("failure-unauthenticated.json"), failure).error.code).toBe(CodeUnauthenticated);
  });

  it("refuses an error code outside the closed set", () => {
    expect(() => decode(fixture("failure-unknown-code.json"), failure)).toThrow(/not an error code/);
  });

  it("refuses a failure that claims ok and a success that does not", () => {
    expect(() => decode('{"ok":true,"requestId":"req_0123456789ab","error":{"code":"internal","message":"x","retryable":false}}', failure)).toThrow(WireError);
    expect(() => decode('{"ok":false,"requestId":"req_0123456789ab","data":{}}', success(object({})))).toThrow(WireError);
  });

  it("separates the minimal health document from the detailed one", () => {
    const minimal: Success<Health> = decode(fixture("success-health-minimal.json"), success(health));
    expect(minimal.data.components).toBeUndefined();
    const detailed = decode(fixture("success-health-detailed.json"), success(health));
    expect(detailed.data.components?.length).toBe(3);
    expect(detailed.data.uptimeSeconds).toBeTypeOf("number");
  });
});

describe("board streams", () => {
  it("decodes the snapshot with twelve topics", () => {
    const snap = decode(fixture("board-snapshot.json"), boardSnapshot);
    expect(Object.keys(snap.revisions)).toHaveLength(12);
    expect(snap.revisions.runs).toBe(4);
  });

  it("names the file of a configuration notice", () => {
    expect(decode(fixture("board-notice-configuration.json"), boardNotice)).toEqual({ topic: "configuration", revision: 3, file: "profile.yml" });
  });
});
