// Clean-room: docs/provenance.md#CR-ENGINE
// Requirement: Drivers for enterprise application suites (Oracle Recruiting Cloud).
import { describe, expect, test } from "vitest";
import { addressScore, chooseOracleRow, choosePostalRow } from "../src/oracle";
import { readInfo, scanControls } from "../src/scan";
import { fakeHost, makeEngine, mount, runFill, summaryOf, ADA, PAD } from "./helpers";
import { infoOf, write } from "./helpers";
import { add, mountOracleAddress, mountOracleSelect } from "./ats-fixtures";

const row = (label: string) => ({ label, cell: document.createElement("div") });

describe("Oracle grid comboboxes", () => {
  test("Grid comboboxes commit only after a hover followed by a click, and the commit is verified on the input's own value", async () => {
    mount("");
    const s = mountOracleSelect({ id: "gender", label: "Gender", options: ["Female", "Male", "Non-binary"] });
    const r = await write("gender", "Female");
    expect(r).toMatchObject({ outcome: "filled", optionLabel: "Female" });
    expect(s.input.value).toBe("Female");
    expect(s.commits).toEqual(["Female"]);
    expect(readInfo(infoOf("gender"))).toBe("Female");
  });

  test("A filtering tenant is re-seeded with the answer's first letter and an absent option abstains with the box empty and closed", async () => {
    mount("");
    const s = mountOracleSelect({ id: "gender", label: "Gender", options: ["Female", "Male"], filter: "substring", lagMs: 5 });
    expect(await write("gender", "Female")).toMatchObject({ outcome: "filled" });
    expect(s.commits).toEqual(["Female"]);
    mount("");
    const t = mountOracleSelect({ id: "gender", label: "Gender", options: ["Female", "Male"], filter: "substring" });
    const r = await write("gender", "Decline");
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(t.input.value).toBe("");
    expect(t.grid.hidden).toBe(true);
  });

  test("A country answered by its code is typed by its name: a substring filter lists no United States for US", async () => {
    mount("");
    const s = mountOracleSelect({ id: "country-34", name: "country", label: "Country", options: ["Australia", "Belarus", "United States", "United States Minor Outlying Islands"], filter: "substring" });
    expect(await write("country-34", "US")).toMatchObject({ outcome: "filled", optionLabel: "United States" });
    expect(s.commits).toEqual(["United States"]);
  });

  test("A postal list's chosen row shows only its leading code, and that is the commit (Chase)", async () => {
    mount("");
    const rows = ["29401, Charleston, Charleston, SC", "29403, Charleston, Charleston, SC", "29407, Charleston, Charleston, SC"];
    const s = mountOracleSelect({ id: "postalCode-58", name: "postalCode", label: "Postal Code", options: rows, display: (r) => r.split(",")[0] as string });
    expect(await write("postalCode-58", "29403")).toMatchObject({ outcome: "filled", optionLabel: "29403, Charleston, Charleston, SC" });
    expect(s.commits).toEqual(["29403, Charleston, Charleston, SC"]);
    expect(s.input.value).toBe("29403");
    // Live, the grid closes a moment after the choice; the write returns once it has, so the read-back holds the code.
    mount("");
    mountOracleSelect({ id: "postalCode-58", name: "postalCode", label: "Postal Code", options: rows, display: (r) => r.split(",")[0] as string, closeDelayMs: 20 });
    expect(await write("postalCode-58", "29403")).toMatchObject({ outcome: "filled" });
    expect(readInfo(infoOf("postalCode-58"))).toBe("29403");
  });

  test("A block with no address master is filled from its Postal Code: the code is typed into the server search and the row of the applicant's city and county is chosen", async () => {
    // Live (Oracle eeho): Address Line 1 is a plain box, and the unfiltered postal list opens at "00000, Baker Island".
    mount(PAD);
    add(`<label for="addressLine1-30">Address Line 1</label><input id="addressLine1-30" name="addressLine1">`);
    const city = mountOracleSelect({ id: "city-31", name: "city", label: "City", options: ["Aaronsburg, Centre, PA", "Durham, Durham, NC"] });
    const zips = ["00000, Baker Island, UM", "27701, Bragtown, Durham, NC", "27701, Durham, Orange, NC", "27701, Durham, Durham, NC", "27702, Durham, Durham, NC"];
    const s = mountOracleSelect({ id: "postalCode-33", name: "postalCode", label: "Postal Code", options: zips, filter: "substring", limit: 3, lagMs: 20, display: (r) => r.split(",")[0] as string });
    const applicant = { ...ADA, candidate: { ...ADA.candidate, address: { ...ADA.candidate.address, county: "Durham County" } } };
    const res = summaryOf(await runFill(makeEngine(fakeHost()), { applicant }));
    expect(s.commits).toEqual(["27701, Durham, Durham, NC"]);
    expect(s.input.value).toBe("27701");
    expect(res.rows.find((r) => r.label === "Postal Code")?.outcome).toBe("filled");
    // The block's other selects are still not the profile's to write.
    expect(city.commits).toEqual([]);
  });

  test("Postal row choice: the exact label, else the one row of the code, else the code's row of the city, then of the county; a tie abstains", () => {
    const rows = ["29492, Charleston, Charleston, SC", "29492, Daniel Island, Charleston, SC", "29401, Charleston, Charleston, SC"].map(row);
    expect(choosePostalRow(rows, "29401")?.label).toBe("29401, Charleston, Charleston, SC");
    expect(choosePostalRow(rows, "29401-1234")?.label).toBe("29401, Charleston, Charleston, SC");
    expect(choosePostalRow(rows, "29492", { city: "Daniel Island" })?.label).toBe("29492, Daniel Island, Charleston, SC");
    expect(choosePostalRow(rows, "29492, Charleston")?.label).toBe("29492, Charleston, Charleston, SC");
    expect(choosePostalRow(rows, "29492, Charleston, Charleston, SC", { city: "Daniel Island" })?.label).toBe("29492, Charleston, Charleston, SC");
    expect(choosePostalRow(rows, "29492")).toBeNull();
    expect(choosePostalRow(rows, "29492", { city: "Mount Pleasant" })).toBeNull();
    expect(choosePostalRow(rows, "29464", { city: "Charleston" })).toBeNull();
    // Live (eeho): with County blank the search lists the code in both counties it spans.
    const spans = ["29401, Charleston, Berkeley, SC", "29401, Charleston, Charleston, SC"].map(row);
    expect(choosePostalRow(spans, "29401", { city: "Charleston", county: "Charleston County" })?.label).toBe("29401, Charleston, Charleston, SC");
    expect(choosePostalRow(spans, "29401", { city: "Charleston" })).toBeNull();
    expect(choosePostalRow(spans, "29401", { city: "Charleston", county: "Dorchester" })).toBeNull();
  });

  test("A typed postal code that the click did not commit is not a write", async () => {
    mount("");
    const rows = ["29401, Charleston, Charleston, SC"];
    const s = mountOracleSelect({ id: "postalCode-58", name: "postalCode", label: "Postal Code", options: rows, filter: "substring", display: (r) => r.split(",")[0] as string });
    // The page swallows the click: the box holds only the code the run typed, and the list stays open.
    s.grid.addEventListener("click", (e) => e.stopPropagation(), true);
    expect(await write("postalCode-58", "29401")).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(s.commits).toEqual([]);
    expect(s.input.value).toBe("");
  });

  test("Row choice: exact, then a unique word-boundary prefix, then the polarity-safe content subset; a tie abstains", () => {
    const rows = ["No, I am not a military spouse", "Not a Veteran", "Protected Veteran", "Veteran"].map(row);
    expect(chooseOracleRow(rows, "No")?.label).toBe("No, I am not a military spouse");
    expect(chooseOracleRow(rows, "Veteran")?.label).toBe("Veteran");
    expect(chooseOracleRow(["Yes", "Yes, I have a disability", "No, I do not have a disability"].map(row), "Yes")?.label).toBe("Yes");
    // A positive wording never picks the negative option.
    expect(chooseOracleRow(["Not a Veteran", "Protected Veteran"].map(row), "I am a protected veteran")?.label).toBe("Protected Veteran");
    expect(chooseOracleRow(["Not a Veteran"].map(row), "I am a veteran")).toBeNull();
    expect(chooseOracleRow(["Asian", "Asian American"].map(row), "Asian or Asian American")?.label).toBe("Asian American");
    expect(chooseOracleRow(["Red Team", "Blue Team"].map(row), "Team")).toBeNull();
  });

  test("The prescan reads the options with the select left empty and closed", async () => {
    mount(PAD);
    const s = mountOracleSelect({ id: "gender", label: "Gender", options: ["Female", "Male"] });
    const host = fakeHost({});
    await runFill(makeEngine(host));
    const q = host.matchRequests.flatMap((r) => r.questions).find((x) => x.text === "Gender");
    expect(q?.options).toEqual(["Female", "Male"]);
    expect(s.input.value).toBe("");
    expect(s.grid.hidden).toBe(true);
  });

  test("The address block's own selects are the profile's and are never opened by the prescan or asked", async () => {
    mount(PAD);
    const city = mountOracleSelect({ id: "city", label: "City", options: ["Durham"] });
    mountOracleSelect({ id: "gender", label: "Gender", options: ["Female"] });
    const host = fakeHost({});
    await runFill(makeEngine(host));
    expect(city.opened()).toBe(0);
    expect(host.matchRequests.flatMap((r) => r.questions).map((q) => q.text)).not.toContain("City");
    const info = scanControls(document).find((i) => i.el.id === "city");
    expect(info?.owner).toBe("profile");
  });

  test("The phone's country-code prefix is never opened by the prescan or asked", async () => {
    mount(PAD);
    const codes = mountOracleSelect({ id: "country-codes-dropdownphoneNumber", name: "phoneNumber", label: "Country Code", options: Array.from({ length: 300 }, (_, i) => `Country ${i} +${i + 1}`) });
    mountOracleSelect({ id: "gender", label: "Gender", options: ["Female"] });
    const host = fakeHost({});
    await runFill(makeEngine(host));
    expect(codes.opened()).toBe(0);
    expect(host.matchRequests.flatMap((r) => r.questions).map((q) => q.text)).not.toContain("Country Code");
  });

  test("The history editor's School is a server search: the whole name is typed and only the row equal to it is taken", async () => {
    // Live: the search lists its ten closest names, so a first-letter seed never reaches a name further down.
    const schools = Array.from({ length: 12 }, (_, i) => `Example College ${i}`).concat(["Example University of Arts", "Example University"]);
    mount("");
    const s = mountOracleSelect({ id: "educationalEstablishment-17", label: "School", options: schools, filter: "substring", limit: 10, lagMs: 20 });
    expect(await write("educationalEstablishment-17", "Example University")).toMatchObject({ outcome: "filled", optionLabel: "Example University" });
    expect(s.commits).toEqual(["Example University"]);
    mount("");
    const t = mountOracleSelect({ id: "educationalEstablishment-17", label: "School", options: ["Example University of Arts"], filter: "substring", lagMs: 20 });
    expect(await write("educationalEstablishment-17", "Example University")).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(t.commits).toEqual([]);
  });

  test("The history editor's Degree is chosen as any degree list is: Master of Science (M.S) is the row that names its level alone (Stanford)", async () => {
    const degrees = ["1. High School Diploma", "5. Bachelor's Degree", "6. Master's Degree", "BS - Bachelor of Science", "MA - Master of Arts", "MS - Master of Science"];
    mount("");
    const s = mountOracleSelect({ id: "contentItemId-51", name: "contentItemId", label: "Degree", options: degrees });
    expect(await write("contentItemId-51", "Master of Science (M.S)")).toMatchObject({ outcome: "filled", optionLabel: "6. Master's Degree" });
    expect(s.commits).toEqual(["6. Master's Degree"]);
  });

});

describe("Oracle address master", () => {
  const target = { city: "Durham", state: "NC", postalCode: "27701" };

  test("Address suggestion tiers: the state must be a whole segment, the city counts as exact, prefix or (several words) embedded, a city only in the street never matches, ZIP+4 matches its prefix", () => {
    expect(addressScore("1 Example Street, Durham, NC 27701", target)).toBe(35);
    expect(addressScore("1 Example Street, Durham, North Carolina (NC) 27701-1234", target)).toBe(35);
    expect(addressScore("1 Example Street, Durham Metro Area, NC", target)).toBe(20);
    expect(addressScore("1 Example Street, Dallas Fort Worth Metroplex, Texas", { city: "Fort Worth", state: "TX" })).toBe(10);
    expect(addressScore("1 Example Street, Research Triangle Durham Region, NC", target)).toBeNull();
    expect(addressScore("1 Example Street, New York, New York", { city: "York", state: "NY" })).toBeNull();
    expect(addressScore("1 Example Street, Wheeling, West Virginia", { city: "Wheeling", state: "VA" })).toBeNull();
    expect(addressScore("1 Example Street, Wheeling, West Virginia", { city: "Wheeling", state: "WV" })).toBe(30);
    expect(addressScore("1 Durham Street, Raleigh, NC 27601", target)).toBeNull();
    expect(addressScore("1 Example Street, Durham, England", target)).toBeNull();
    expect(addressScore("1 Example Street", target)).toBeNull();
  });

  test("Oracle address: only the matching suggestion is committed and the cascaded fields hold its values", async () => {
    mount("");
    const a = mountOracleAddress({
      waves: [[10, ["1 Example Street, Houston, TX 77001", "1 Example Street, Durham, England", "1 Example Street, Durham, NC 27701", "1 Example Street, Raleigh, NC 27601"]]],
    });
    const r = await write("addressLine1", "1 Example Street", { address: { city: "Durham", state: "NC", postalCode: "27701" } });
    expect(r.outcome).toBe("filled");
    expect(a.commits).toEqual(["1 Example Street, Durham, NC 27701"]);
    expect(a.fields.city!.value).toBe("Durham");
    expect(a.fields.region1!.value).toBe("NC");
    expect(a.fields.postalCode!.value).toBe("27701");
  });

  test("Late Oracle suggestions: the matching row that arrives late is committed and a phone-code list that is also open is ignored", async () => {
    mount("");
    const a = mountOracleAddress({
      waves: [
        [10, ["1 Example Street, Houston, TX 77001"]],
        [120, ["1 Example Street, Durham, NC 27701"]],
      ],
      phoneCodes: true,
    });
    const r = await write("addressLine1", "1 Example Street", { address: { city: "Durham", state: "NC", postalCode: "27701" } });
    expect(r.outcome).toBe("filled");
    expect(a.commits).toEqual(["1 Example Street, Durham, NC 27701"]);
  });

  test("A cascade field the page had filled before the street was typed does not prove the commit: a swallowed click is not a write, and another block's city is not this master's", async () => {
    mount(`<label for="other">City</label><input id="other" name="city" value="Elsewhere">`);
    const a = mountOracleAddress({ waves: [[10, ["1 Example Street, Durham, NC 27701"]]] });
    // The page's parse filled the city while Address Line 1 was empty, and the page swallows the click.
    a.fields.city!.value = "Raleigh";
    document.getElementById("addr-grid")!.addEventListener("click", (e) => e.stopPropagation(), true);
    const r = await write("addressLine1", "1 Example Street", { address: { city: "Durham", state: "NC", postalCode: "27701" } });
    expect(r).toMatchObject({ outcome: "failed", reason: "write-not-committed" });
    expect(a.commits).toEqual([]);
  });

  test("No suggestion in the applicant's city and state leaves the box empty", async () => {
    mount("");
    const a = mountOracleAddress({ waves: [[10, ["1 Example Street, Houston, TX 77001"]]] });
    const r = await write("addressLine1", "1 Example Street", { address: { city: "Durham", state: "NC" } });
    expect(r).toMatchObject({ outcome: "abstained", reason: "option-not-found" });
    expect(a.input.value).toBe("");
  });

  test("The street is typed one key at a time and an already-filled master keeps ownership across passes", async () => {
    mount("");
    const a = mountOracleAddress({ waves: [[10, ["1 Example Street, Durham, NC 27701"]]] });
    const res = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(a.typedAt.length).toBe("1 Example Street".length);
    expect(a.commits).toHaveLength(1);
    expect(a.fields.city!.value).toBe("Durham");
    expect(res.rows.find((r) => r.label.startsWith("Address Line 1"))?.outcome).toBe("filled");
    // A second run does not re-drive the master and the cascade survives.
    const again = summaryOf(await runFill(makeEngine(fakeHost()), { applicant: ADA }));
    expect(a.commits).toHaveLength(1);
    expect(a.fields.city!.value).toBe("Durham");
    expect(again.rows.find((r) => r.label.startsWith("Address Line 1"))?.outcome).toBe("prefilled");
  });
});
