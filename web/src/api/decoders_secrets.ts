// Decoders for the secret management routes. Each mirrors a struct of
// internal/api/wire; the object decoder rejects unknown fields. None of the
// shapes carries a secret value.
import type { AlternateAdded, SecretEntry, SecretList, SecretRegistryRow, SecretStatus, SecretTest } from "../wire";
import { bool, num, object, orEmpty, safeInt, str, time, type Decoder } from "./decode";

const registryRow: Decoder<SecretRegistryRow> = object<SecretRegistryRow>(
  { kind: str, name: str, description: str, env: str, required: bool },
  { setupStep: str },
);

const secretEntry: Decoder<SecretEntry> = object<SecretEntry>(
  { name: str, kind: str, present: bool },
  {
    label: str, email: str, host: str, system: str, tenant: str, position: num, source: str, problem: str,
    updatedAt: time, lastSigninAt: time, lastOutcome: str,
  },
);

export const secretList: Decoder<SecretList> = object<SecretList>({ registry: orEmpty(registryRow), entries: orEmpty(secretEntry) });

export const secretStatus: Decoder<SecretStatus> = object<SecretStatus>({ name: str, present: bool }, { source: str, shadowedBy: str });

export const secretTest: Decoder<SecretTest> = object<SecretTest>({ name: str, present: bool, live: str }, { source: str, balanceCents: safeInt, belowFloor: bool, detail: str });

export const alternateAdded: Decoder<AlternateAdded> = object<AlternateAdded>({ id: str, name: str });
