# Wire fixtures

Synthetic JSON documents shared by the Go tests of `internal/api/wire` and the
TypeScript consumer tests (`web/src/api/decode.test.ts`). `manifest.json` lists every case:

| Field | Meaning |
|---|---|
| `file` | The document, relative to this directory. |
| `type` | The wire type that decodes it (`Failure` and `SuccessHealth` are envelope shapes). |
| `valid` | `false` when decoding must fail: unknown code, zone-less timestamp, integer outside the safe range, unknown field. |
| `canonical` | For a valid case that does not re-encode to itself, the file holding the expected re-encoding (offsets become UTC). |

The cases cover timestamps and offsets, enum errors, the safe integer range
and error envelopes.
Type generation is not runtime validation; both languages decode these files
at runtime.
