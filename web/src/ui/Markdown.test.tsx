import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";
import { mount, q, qa } from "../testing/dom";

describe("Markdown", () => {
  it("shows raw HTML as text", async () => {
    await mount(<Markdown source={'Hello <img src=x onerror="alert(1)"> and <script>alert(2)</script>'} />);
    expect(q("script")).toBeNull();
    expect(q("img")).toBeNull();
    expect(q(".markdown")?.textContent).toContain("<script>alert(2)</script>");
  });

  it("refuses script links and opens web links safely", async () => {
    await mount(<Markdown source={"[bad](javascript:alert(1)) [good](https://example.com/a)"} />);
    const anchors = qa<HTMLAnchorElement>("a");
    expect(anchors).toHaveLength(1);
    expect(anchors[0]).toMatchObject({ rel: "noopener noreferrer", target: "_blank" });
    expect(q(".markdown")?.textContent).toContain("bad");
  });

  it("renders headings, lists, tables and code, and never treats code as markup", async () => {
    await mount(<Markdown source={"# T\n\n- a\n- b\n\n1. x\n\n| H |\n| --- |\n| c |\n\n```\n<b>raw</b>\n```\n"} />);
    expect(q("h2")?.id).toBe("t");
    expect(qa("ul li")).toHaveLength(2);
    expect(qa("ol li")).toHaveLength(1);
    expect(q("table th")?.textContent).toBe("H");
    expect(q("pre code")?.textContent).toBe("<b>raw</b>");
    expect(q<HTMLElement>("pre")?.tabIndex).toBe(0);
    expect(q("pre b")).toBeNull();
  });

  it("refuses an image whose address a browser resolves to another origin", async () => {
    const hostile = ["/\\evil.example/p.png", "/\\\\evil.example/p.png", "\\\\evil.example/p.png", "/&#9;/evil.example/p.png", "/&#10;/evil.example/p.png", "/&#x5C;evil.example/p.png", "//evil.example/p.png", "/\\&#9;evil.example/p.png"];
    for (const src of hostile) {
      await mount(<Markdown source={`![x](${src})`} />);
      expect(qa("img"), src).toHaveLength(0);
    }
    await mount(<Markdown source={"![ok](/api/files/a.png?x=1)"} />);
    expect(qa<HTMLImageElement>("img").map((i) => i.getAttribute("src"))).toEqual(["/api/files/a.png?x=1"]);
  });

  it("decodes entities as text and never as markup", async () => {
    await mount(<Markdown source={"a &lt;script&gt;alert(1)&lt;/script&gt; &amp; &#65;&#x42; &quot;q&quot; &nbsp;|&bogus; &#0; &#xD800;"} />);
    expect(q("script")).toBeNull();
    expect(q(".markdown")?.textContent).toContain('a <script>alert(1)</script> & AB "q"');
    expect(q(".markdown")?.textContent).toContain("&bogus;");
  });

  it("links an autolink only through the same scheme check", async () => {
    await mount(<Markdown source={"<https://example.com/a> <javascript:alert(1)> <script> <mailto:alex@example.com> <data:text/html,x>"} />);
    const anchors = qa<HTMLAnchorElement>("a");
    expect(anchors.map((a) => a.getAttribute("href"))).toEqual(["https://example.com/a", "mailto:alex@example.com"]);
    expect(anchors[0]).toMatchObject({ rel: "noopener noreferrer", target: "_blank" });
    expect(q(".markdown")?.textContent).toContain("<javascript:alert(1)>");
    expect(q("script")).toBeNull();
  });

  it("never emits a script scheme, a handler attribute or an off-origin image for hostile links", async () => {
    const cases = [
      "[a](javascript:alert(1))", "[a](JaVaScRiPt:alert(1))", "[a](&#106;avascript:alert(1))", "[a](&#x6A;avascript:alert(1))",
      "[a](java&#9;script:alert(1))", "[a](data:text/html,x)", "[a](vbscript:msgbox(1))", "[a](file:///etc/passwd)", "[a](blob:http://127.0.0.1/abc)",
      "[a][ref]\n\n[ref]: javascript:alert(1)", "![a](javascript:alert(1))", "![a](data:image/svg+xml,x)", "![a](//evil.example/p.png)",
      "![a](/\\evil.example/p.png)", "![x\" onerror=\"alert(1)](/a.png)", "<img src=x onerror=alert(1)>", "&lt;img src=x onerror=alert(1)&gt;",
    ];
    for (const c of cases) {
      await mount(<Markdown source={c} />);
      const html = q(".markdown")!.innerHTML;
      expect(html, c).not.toMatch(/href="(javascript|data|vbscript|file|blob):/i);
      expect(qa("*").some((e) => e.getAttributeNames().some((a) => a.startsWith("on"))), c).toBe(false);
      expect(html, c).not.toMatch(/<(script|svg|iframe)/i);
      expect(qa("img").every((i) => i.getAttribute("src")?.startsWith("/") && !i.getAttribute("src")!.startsWith("//") && !i.getAttribute("src")!.includes("\\")), c).toBe(true);
    }
  });
});
