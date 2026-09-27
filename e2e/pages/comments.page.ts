import { type Locator, expect } from "@playwright/test";
import { BasePage } from "./base.page";

/** Page object for the commentary layer on an entity detail view
 *  (TKT-FIO205). Selectors mirror the components' stable class prefixes:
 *  `.ci-` for the per-field indicator and its popover, `.tsc-` for
 *  select-to-comment, `.tcp-` for a clicked highlight's thread, `.bco-` for the
 *  image/diagram overlay, and `.comments-panel` for the collapsed catch-all. */
export class CommentsPage extends BasePage {
  /** Open an entity's detail view and wait for its heading. */
  async openEntity(type: string, id: string) {
    await this.navigateTo(`/entity/${type}/${id}`);
    await expect(this.page.locator("h1").first()).toBeVisible();
  }

  // ── Per-field indicators ───────────────────────────────────────────

  /** The comment indicator beside a property label. */
  fieldIndicator(property: string): Locator {
    return this.page
      .locator(".property-item")
      .filter({ hasText: property })
      .locator(".ci-btn")
      .first();
  }

  /** Open a field's comment popover. */
  async openField(property: string) {
    await this.fieldIndicator(property).click();
    await expect(this.page.locator(".ci-pop")).toBeVisible();
  }

  /** Post a comment from the currently-open field popover. */
  async postFieldComment(body: string) {
    await this.page.locator(".ci-composer .ci-input").fill(body);
    await this.page.locator(".ci-composer button[type=submit]").click();
    // The composer clears on success, which is the settle signal.
    await expect(this.page.locator(".ci-composer .ci-input")).toHaveValue("");
  }

  /** Delete the first comment in the open field popover, confirming the modal. */
  async deleteFirstFieldComment() {
    await this.page.locator(".ci-pop .ci-mini--danger").first().click();
    await this.page
      .locator(".modal button, [role=dialog] button")
      .filter({ hasText: /^Delete$/ })
      .last()
      .click();
  }

  /** Close whatever popover is open. */
  async dismissPopover() {
    await this.page.keyboard.press("Escape");
  }

  /** Comment bodies visible in the open field popover. */
  fieldCommentBodies(): Locator {
    return this.page.locator(".ci-pop .ci-body");
  }

  /** The count shown on a field's indicator, or null when it has none. */
  async fieldCommentCount(property: string): Promise<string | null> {
    const badge = this.fieldIndicator(property).locator(".ci-count");
    return (await badge.count()) === 0 ? null : badge.innerText();
  }

  // ── Select-to-comment on the body ──────────────────────────────────

  /** Select a phrase in the rendered body, so the comment affordance appears.
   *
   *  Uses a DOM Range rather than a drag: a drag over rendered markdown is
   *  pixel-dependent and flakes, while the anchor under test is the SELECTED
   *  TEXT, which a Range specifies exactly. */
  async selectBodyText(phrase: string) {
    await this.page.evaluate((needle) => {
      const host = document.querySelector(".content-body");
      if (!host) throw new Error("no rendered body on this page");
      const walker = document.createTreeWalker(host, NodeFilter.SHOW_TEXT);
      let node: Node | null;
      while ((node = walker.nextNode())) {
        const i = (node.textContent ?? "").indexOf(needle);
        if (i === -1) continue;
        const range = document.createRange();
        range.setStart(node, i);
        range.setEnd(node, i + needle.length);
        const sel = window.getSelection();
        sel?.removeAllRanges();
        sel?.addRange(range);
        document.dispatchEvent(new Event("selectionchange"));
        return;
      }
      throw new Error(`phrase not found in body: ${needle}`);
    }, phrase);
  }

  /** The "Comment" button offered for a valid selection. */
  selectionButton(): Locator {
    return this.page.locator(".tsc-btn");
  }

  /** The notice shown when a selection cannot be anchored. */
  blockedNotice(): Locator {
    return this.page.locator(".tsc-blocked");
  }

  /** Comment on the current selection. */
  async commentOnSelection(body: string) {
    await this.selectionButton().click();
    await this.page.locator(".tsc-input").fill(body);
    await this.page.locator(".tsc-submit").click();
    // The composer unmounts on success.
    await expect(this.page.locator(".tsc-form")).toHaveCount(0);
  }

  /** Suggest replacing the current selection with `replacement`. */
  async suggestOnSelection(replacement: string) {
    await this.selectionButton().click();
    await this.page.locator(".tsc-suggest").click();
    await this.page.locator("#tsc-replacement").fill(replacement);
    await this.page.locator(".tsc-submit").click();
    await expect(this.page.locator(".tsc-form")).toHaveCount(0);
  }

  /** The text a suggestion in the open thread removes. */
  threadDiffRemoved(): Locator {
    return this.page.locator(".tcp [data-testid=suggestion-diff] del");
  }

  /** The text a suggestion in the open thread adds. */
  threadDiffAdded(): Locator {
    return this.page.locator(".tcp [data-testid=suggestion-diff] ins");
  }

  /** The rendered entity body. */
  entityBody(): Locator {
    return this.page.locator(".content-body");
  }

  /** Accept the suggestion in the open highlight thread. */
  async acceptInThread() {
    await this.page
      .locator(".tcp")
      .getByRole("button", { name: "Accept", exact: true })
      .click();
  }

  // ── Highlights ─────────────────────────────────────────────────────

  /** Highlights rendered over commented body text. */
  highlights(): Locator {
    return this.page.locator(".content-body mark[data-comment-id]");
  }

  /** Open the thread for a highlight by its text. */
  async openHighlight(text: string) {
    await this.highlights().filter({ hasText: text }).first().click();
    await expect(this.page.locator(".tcp")).toBeVisible();
  }

  /** Comment bodies in the open highlight thread. */
  highlightCommentBodies(): Locator {
    return this.page.locator(".tcp .tcp-body");
  }

  /** Reply within the open highlight thread. */
  async replyInThread(body: string) {
    await this.page.locator(".tcp-reply textarea").fill(body);
    await this.page.locator(".tcp-reply button[type=submit]").click();
    await expect(this.page.locator(".tcp-reply textarea")).toHaveValue("");
  }

  // ── Image / diagram blocks ─────────────────────────────────────────

  /** Comment affordances over images and diagrams. */
  blockButtons(): Locator {
    return this.page.locator(".bco-btn");
  }

  // ── The catch-all panel ────────────────────────────────────────────

  /** The collapsed "All comments" header. */
  panelHeader(): Locator {
    return this.page.locator(".comments-panel .panel-header");
  }

  /** Expand the catch-all panel. */
  async expandPanel() {
    await this.panelHeader().click();
    await expect(this.page.locator(".comments-panel .comment-list")).toBeVisible();
  }

  /** Comment rows inside the expanded panel. */
  panelComments(): Locator {
    return this.page.locator(".comments-panel .comment");
  }

  /** The panel's summary text ("4 total · 2 not on a field"). */
  panelSummary(): Locator {
    return this.page.locator(".comments-panel .panel-summary");
  }
}
