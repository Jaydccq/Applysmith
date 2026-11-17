import type { Tone } from "../discovery/format";

export const TIERS = ["hot", "warm", "watch", "cold"] as const;
export const SIGNAL_STATES = ["new", "watching", "dismissed"] as const;
export const SIGNAL_SOURCES = ["signals-edgar", "signals-alerts", "signals-hn", "signals-hn-hiring", "signals-web", "signals-x"] as const;

export function tierTone(tier: string): Tone {
  switch (tier) {
    case "hot":
      return "bad";
    case "warm":
      return "warn";
    case "watch":
      return "good";
  }
  return "neutral";
}
