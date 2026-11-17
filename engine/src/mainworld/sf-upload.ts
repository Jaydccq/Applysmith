// Clean-room: docs/provenance.md#CR-ENGINE
// SuccessFactors' own uploader overwrites a file assigned from outside, so the
// file is put on the input from the page's world, only on the system's own
// uploader inputs, only while a document is staged (60 seconds at most, and
// gone once it was delivered or dropped), and "attached" is reported only
// when a file really landed.

import { resolve } from "./components";

type W = Window & typeof globalThis;

interface Staged {
  file: File;
  until: number;
}

export class SfUpload {
  private staged: Staged | null = null;

  constructor(private readonly win: W) {}

  stage(name: string, type: string, base64: string, ttlMs: number): { staged: true } {
    const bin = this.win.atob(base64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    this.staged = { file: new this.win.File([bytes], name, { type }), until: Date.now() + Math.min(ttlMs, 60_000) };
    return { staged: true };
  }

  /** Drops the staged document: an upload that did not take it leaves nothing behind for a later click. */
  clear(): void {
    this.staged = null;
  }

  private current(): File | null {
    if (!this.staged) return null;
    if (Date.now() > this.staged.until) {
      this.staged = null;
      return null;
    }
    return this.staged.file;
  }

  /**
   * Only the system's own uploader, by the rule the core's isSfUploader applies: a name starting with fileData, the
   * fileUpload class, or the form of the uploader, which holds the file alone beside hidden fields.
   */
  isUploader(input: Element): boolean {
    if (input.localName !== "input" || (input as HTMLInputElement).type !== "file") return false;
    const form = (input as HTMLInputElement).form;
    const uploaderForm = form !== null && /^(form_|fil)/.test(form.id) && Array.from(form.elements).every((e) => e === input || (e as HTMLInputElement).type === "hidden");
    return /^fileData/.test(input.getAttribute("name") ?? "") || input.classList.contains("fileUpload") || uploaderForm || input.closest(".fileUploaderForm") !== null;
  }

  private fill(input: HTMLInputElement): boolean {
    const file = this.current();
    if (!file || !this.isUploader(input)) return false;
    const dt = new this.win.DataTransfer();
    dt.items.add(file);
    input.files = dt.files;
    input.dispatchEvent(new this.win.Event("input", { bubbles: true }));
    input.dispatchEvent(new this.win.Event("change", { bubbles: true }));
    return (input.files?.length ?? 0) > 0;
  }

  attach(selector: string): { attached: boolean; reason?: string } {
    const input = resolve(this.win, selector);
    if (!input) return { attached: false, reason: "no-element" };
    if (!this.current()) return { attached: false, reason: "nothing-staged" };
    if (!this.isUploader(input)) return { attached: false, reason: "not-an-uploader" };
    const ok = this.fill(input as HTMLInputElement);
    if (ok) this.staged = null;
    return ok ? { attached: true } : { attached: false, reason: "not-landed" };
  }

  /** A programmatic click on an uploader input is answered with the staged file instead of the native picker. */
  install(): () => void {
    const proto = this.win.HTMLInputElement.prototype;
    const orig = proto.click;
    const self = this;
    proto.click = function patchedClick(this: HTMLInputElement): void {
      try {
        if (self.isUploader(this) && self.current() && self.fill(this)) {
          // Delivered once: the next click on an uploader is the person's own and opens the picker.
          self.staged = null;
          return;
        }
      } catch {
        /* fall through to the page's own click */
      }
      return orig.call(this);
    };
    return () => {
      proto.click = orig;
    };
  }
}
