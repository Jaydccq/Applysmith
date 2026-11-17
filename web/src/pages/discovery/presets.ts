// Named scan presets are a convenience of this browser: they are kept in local
// storage, per source, and a blocked store only loses them.
import type { Draft } from "./request";

const KEY = "applysmith-scan-presets";

export interface Preset {
  name: string;
  source: string;
  draft: Draft;
}

function readAll(): Preset[] {
  try {
    const raw = window.localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? (parsed as Preset[]) : [];
  } catch {
    return [];
  }
}

function writeAll(all: Preset[]): void {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    // The preset stays for this visit only.
  }
}

export const loadPresets = (source: string): Preset[] => readAll().filter((p) => p.source === source);

export function savePreset(source: string, name: string, draft: Draft): Preset[] {
  writeAll([...readAll().filter((p) => !(p.source === source && p.name === name)), { name, source, draft }]);
  return loadPresets(source);
}

export function deletePreset(source: string, name: string): Preset[] {
  writeAll(readAll().filter((p) => !(p.source === source && p.name === name)));
  return loadPresets(source);
}
