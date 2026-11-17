// Clean-room: docs/provenance.md#CR-ENGINE
// Requirements: Pipeline stages and their order (stale prose box); Profile-field pass (parser-prefilled values);
// Resilience to re-renders and user edits (state and country refill).
import { describe, expect, test } from "vitest";
import { ADA, fakeHost, makeEngine, mount, PAD, runFill, setUrl } from "./helpers";

const APPLICANT = { ...ADA, history: { education: [{ school: "Example University", degree: "Bachelor of Science", field: "Computer Science" }] } };

describe("Stale prose", () => {
  test("A prose box that holds exactly one bare profile value its question does not ask for is cleared; a question that names the field, a one-line input and a person's own prose are left", async () => {
    mount(`<label for="sum">Summary</label><textarea id="sum">Bachelor of Science</textarea>
      <label for="deg">Highest degree earned</label><textarea id="deg">Bachelor of Science</textarea>
      <label for="line">Anything else</label><input id="line" value="Bachelor of Science">
      <label for="own">About you</label><textarea id="own">I enjoy writing small reliable tools.</textarea>${PAD}`);
    await runFill(makeEngine(fakeHost()), { applicant: APPLICANT });
    expect((document.getElementById("sum") as HTMLTextAreaElement).value).toBe("");
    expect((document.getElementById("deg") as HTMLTextAreaElement).value).toBe("Bachelor of Science");
    expect((document.getElementById("line") as HTMLInputElement).value).toBe("Bachelor of Science");
    expect((document.getElementById("own") as HTMLTextAreaElement).value).toBe("I enjoy writing small reliable tools.");
  });
});

describe("Parser-prefilled identity", () => {
  const form = `<label for="fn">First name</label><input id="fn" value="Wrong">
    <label for="ph">Phone number</label><input id="ph" type="tel" value="(555) 0100">
    <label for="ci">City</label><input id="ci" value="durham">
    <label for="em">Email</label><input id="em" type="email" value="old@example.org">`;

  test("On a host whose parser guesses, the applicant's value wins over a different guess and a value that is the same is kept", async () => {
    setUrl("https://example.wd5.myworkdayjobs.com/en-US/site/job/x/apply");
    mount(form);
    await runFill(makeEngine(fakeHost()), { applicant: ADA });
    expect((document.getElementById("fn") as HTMLInputElement).value).toBe("Ada");
    expect((document.getElementById("em") as HTMLInputElement).value).toBe("ada@example.com");
    expect((document.getElementById("ci") as HTMLInputElement).value).toBe("durham");
  });

  test("Elsewhere a committed value is never overwritten", async () => {
    mount(form);
    await runFill(makeEngine(fakeHost()), { applicant: ADA });
    expect((document.getElementById("fn") as HTMLInputElement).value).toBe("Wrong");
  });
});
