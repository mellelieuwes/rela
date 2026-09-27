import { type Page, type Locator, expect } from "@playwright/test";
import { BasePage } from "./base.page";

export class FormPage extends BasePage {
  readonly form: Locator;
  readonly submitButton: Locator;
  readonly cancelButton: Locator;
  readonly titleInput: Locator;

  constructor(page: Page) {
    super(page);
    this.form = page.locator("form");
    // The PRIMARY submit only. `has-text` is a substring match, so a bare
    // "Create" clause also matches "Create & add another" (TKT-7YHKD1) and
    // every submit in the suite becomes a strict-mode violation. The
    // secondary action is deliberately type="button" (two submit buttons in
    // one form make Enter ambiguous), so the type selector already excludes
    // it; the text clauses are narrowed to exact matches for the same reason.
    this.submitButton = page.locator(
      'button[type="submit"], button:text-is("Save"), button:text-is("Create")',
    );
    this.cancelButton = page.locator('button:has-text("Cancel")');
    this.titleInput = page.locator("#field-title");
  }

  /** The app shell chrome — present whenever the SPA is mounted. */
  get appChrome(): Locator {
    return this.page.locator(".sidebar, nav").first();
  }

  /** Navigate to a list view (used to seed in-app history before a form). */
  async navigateToList(listId: string) {
    await this.navigateTo(`/list/${listId}`);
    await this.waitForSpinnerToDisappear();
  }

  async navigateToCreateForm(formId: string) {
    await this.navigateTo(`/form/${formId}`);
    await this.waitForSpinnerToDisappear();
    await expect(this.titleInput).toBeVisible();
  }

  // --- Wizard (multi-step) form helpers ---

  private get wizardSteps(): Locator {
    return this.page.locator(".wizard-steps .wizard-step-pill");
  }

  private get wizardNextBtn(): Locator {
    return this.page.locator('.form-actions button:has-text("Next step")');
  }

  private get wizardBackBtn(): Locator {
    return this.page.locator('.form-actions button:has-text("Prev step")');
  }

  /** Titles of the currently-rendered wizard steps, in order. */
  async wizardStepTitles(): Promise<string[]> {
    return this.wizardSteps.locator(".wizard-step-title").allInnerTexts();
  }

  /** The 0-based index of the active wizard step (from the `active` pill). */
  async activeStepIndex(): Promise<number> {
    const count = await this.wizardSteps.count();
    for (let i = 0; i < count; i++) {
      const cls = (await this.wizardSteps.nth(i).getAttribute("class")) ?? "";
      if (cls.includes("active")) return i;
    }
    return -1;
  }

  /** Toggle a checkbox field on/off by property name. */
  async setCheckbox(name: string, checked: boolean) {
    const box = this.page.locator(`#field-${name}`);
    await box.setChecked(checked);
  }

  /** Click a wizard step pill by its 0-based visible index. */
  async clickStep(index: number) {
    await this.wizardSteps.nth(index).click();
  }

  async clickNext() {
    await this.wizardNextBtn.click();
  }

  /** Click the wizard's Create/Save submit button (last step). */
  async clickCreate() {
    await this.page.locator('.form-actions button[type="submit"]').click();
  }

  async clickBack() {
    await this.wizardBackBtn.click();
  }

  async isFieldVisible(name: string): Promise<boolean> {
    return this.page
      .locator(`#field-${name}`)
      .isVisible()
      .catch(() => false);
  }

  /** Value of the `step` query param on the current URL, or null. */
  readStepParam(): string | null {
    return new URL(this.page.url()).searchParams.get("step");
  }

  /** 0-based indices of wizard step pills currently flagged with an error. */
  async erroredStepIndices(): Promise<number[]> {
    const count = await this.wizardSteps.count();
    const out: number[] = [];
    for (let i = 0; i < count; i++) {
      const cls = (await this.wizardSteps.nth(i).getAttribute("class")) ?? "";
      if (cls.includes("has-errors")) out.push(i);
    }
    return out;
  }

  /** Text of the wizard's submit-time error summary, or null if not shown. */
  async errorSummaryText(): Promise<string | null> {
    const el = this.page.locator(".wizard-error-summary");
    if (!(await el.isVisible().catch(() => false))) return null;
    return el.innerText();
  }

  /** Fill a set of property fields keyed by property name. */
  async fillFields(values: Record<string, string>) {
    for (const [name, value] of Object.entries(values)) {
      await this.fillField(name, value);
    }
  }

  /** Fill a set of select fields keyed by property name. */
  async selectFields(values: Record<string, string>) {
    for (const [name, value] of Object.entries(values)) {
      await this.selectField(name, value);
    }
  }

  /** Submit the form and wait for the create POST to succeed. Returns the entity response. */
  async submitAndExpectCreate(plural: string): Promise<{ id: string }> {
    const [response] = await Promise.all([
      this.page.waitForResponse(
        (r) =>
          r.url().includes(`/api/v1/${plural}`) &&
          r.request().method() === "POST" &&
          r.status() === 201,
      ),
      this.submitButton.click(),
    ]);
    expect(response.ok()).toBeTruthy();
    return response.json() as Promise<{ id: string }>;
  }

  /**
   * Click "Create & add another" (TKT-7YHKD1) and wait for the create POST.
   * Unlike submitAndExpectCreate this does NOT navigate — the form stays put
   * and blanks itself for the next record.
   */
  async addAnotherAndExpectCreate(plural: string): Promise<{ id: string }> {
    // Arm the wait BEFORE the click and keep the predicate loose on status:
    // the create form POSTs to the SAME path with ?dry_run=true for its
    // staged-affordance check (on mount, and debounced on every edit), so the
    // predicate must exclude those or it resolves on a dry-run instead.
    const responsePromise = this.page.waitForResponse(
      (r) =>
        r.url().includes(`/api/v1/${plural}`) &&
        !r.url().includes("dry_run") &&
        r.request().method() === "POST",
      // Above the 5s default: unlike a plain submit, a repeat call has to wait
      // out the PREVIOUS record's in-place reset (which awaits an affordance
      // dry-run) before its own create is even issued. Under a loaded parallel
      // run that stacks up, and the default budget flakes.
      { timeout: 15000 },
    );
    const button = this.page.locator(
      '.form-actions button:has-text("Create & add another")',
    );
    // The 24px fixed .status-bar footer sits over the bottom of the viewport,
    // and this button is the LAST in the action row — so at some scroll
    // positions it lands under the footer and Playwright's actionability check
    // reports "footer intercepts pointer events". Scroll it clear first rather
    // than forcing the click, which would mask a genuine overlay.
    await button.scrollIntoViewIfNeeded();
    const [response] = await Promise.all([responsePromise, button.click()]);
    expect(response.ok()).toBeTruthy();
    // The action does not navigate; it resets the form IN PLACE. Wait for the
    // blank title so the caller types record N+1 into a settled form rather
    // than racing the reset that is about to clear it.
    await expect(this.titleInput).toHaveValue("", { timeout: 15000 });
    return response.json() as Promise<{ id: string }>;
  }

  async navigateToEditForm(formId: string, entityId: string) {
    await this.navigateTo(`/form/${formId}/${entityId}`);
    await this.waitForSpinnerToDisappear();
  }

  /** Assert the form route guard rendered the "not editable" inline
   *  message (instead of the form). Triggered when the server says
   *  `_actions.update === false` for the target entity — AC10 of the
   *  read-only payoff. */
  async expectNotEditableMessage(timeoutMs = 10_000) {
    const banner = this.page.locator(".not-editable-state");
    await expect(banner).toBeVisible({ timeout: timeoutMs });
    await expect(banner).toContainText(/not editable/i);
  }

  /** Wait for the URL to be the edit form for the given form/entity. Used
   *  when navigation is triggered by another page (e.g. Edit button on the
   *  document view). */
  async expectAtFormUrl(formId: string, entityId: string) {
    await this.page.waitForURL(new RegExp(`/form/${formId}/${entityId}`), {
      timeout: 10000,
    });
    await this.waitForSpinnerToDisappear();
  }

  /** Decoded value of the `return_to` query param on the current URL, or
   *  null if absent. Useful for asserting on the form's return target
   *  without coupling to URL-encoding details (RR-BS0O4). */
  readReturnTo(): string | null {
    return new URL(this.page.url()).searchParams.get("return_to");
  }

  async fillField(name: string, value: string) {
    const field = this.page.locator(`#field-${name}`);
    await field.fill(value);
  }

  async selectField(name: string, value: string) {
    const field = this.page.locator(`#field-${name}`);
    await field.selectOption(value);
  }

  async fillTitle(value: string) {
    await this.titleInput.fill(value);
  }

  async fillMarkdown(value: string) {
    // The markdown editor is a ProseMirror contenteditable, so it takes real
    // keyboard input rather than needing an editor-instance handle.
    const surface = this.proseMirror;
    if (await surface.isVisible({ timeout: 1000 }).catch(() => false)) {
      await surface.click();
      await this.page.keyboard.press("ControlOrMeta+a");
      await this.page.keyboard.type(value);
    } else {
      // Fallback to textarea
      const textarea = this.page.locator(
        'textarea[name="content"], textarea.markdown-input',
      );
      await textarea.fill(value);
    }
  }

  async addRelation(relationName: string, targetId: string) {
    // Legacy <select> path (kept for any form that still renders one) —
    // for the default RelationPicker widget we delegate to
    // `pickInRelationPicker` so both helpers share the same combobox-
    // scoped selector strategy.
    const relationSection = this.page
      .locator(".relation-field, .form-field")
      .filter({ hasText: relationName });
    const select = relationSection.locator("select");
    if (await select.isVisible().catch(() => false)) {
      await select.selectOption(targetId);
      return;
    }
    await this.pickInRelationPicker(relationSection, targetId, targetId);
  }

  async submit() {
    const startUrl = this.page.url();
    // Wait briefly for the submit button to appear; in create mode the
    // form always has it. In autosave edit mode it never appears.
    const submitVisible = await this.submitButton
      .first()
      .waitFor({ state: "visible", timeout: 2000 })
      .then(() => true)
      .catch(() => false);
    if (submitVisible) {
      await this.submitButton.first().click();
      // Submit is a client-side fetch followed by a router.push on success.
      // Wait briefly for the URL to change; if validation fails we stay on the form.
      await this.page
        .waitForURL((url) => url.toString() !== startUrl, { timeout: 2000 })
        .catch(() => {});
      return;
    }
    // TKT-E6094 autosave: no explicit Submit. Blur, wait for the autosave
    // PATCH(es) to drain, then navigate back; the route guard flushes any
    // pending edits before the URL changes.
    await this.page.evaluate(() =>
      (document.activeElement as HTMLElement | null)?.blur(),
    );
    await this.page.waitForTimeout(1000);
    await this.page.goBack();
    await this.page
      .waitForURL((url) => url.toString() !== startUrl, { timeout: 2000 })
      .catch(() => {});
  }

  async cancel() {
    const startUrl = this.page.url();
    await this.cancelButton.click();
    await this.page
      .waitForURL((url) => url.toString() !== startUrl, { timeout: 2000 })
      .catch(() => {});
  }

  async expectValidationError(message: string) {
    await expect(
      this.page
        .locator('.error, .validation-error, [role="alert"]')
        .filter({ hasText: message }),
    ).toBeVisible();
  }

  async expectFieldValue(name: string, value: string) {
    const field = this.page.locator(`#field-${name}`);
    await expect(field).toHaveValue(value);
  }

  async expectFormTitle(title: string) {
    await expect(
      this.page.locator("h1, h2, .form-title").filter({ hasText: title }),
    ).toBeVisible();
  }

  async getFieldValue(name: string): Promise<string> {
    const field = this.page.locator(`#field-${name}`);
    return field.inputValue();
  }

  async hasField(name: string): Promise<boolean> {
    return this.page.locator(`#field-${name}`).isVisible();
  }

  async hasSubmitButton(): Promise<boolean> {
    return this.submitButton.first().isVisible();
  }

  async hasValidationError(): Promise<boolean> {
    const error = this.page
      .locator('.error, .validation-error, .field-error, [role="alert"]')
      .first();
    return error.isVisible().catch(() => false);
  }

  async getSelectOptions(name: string): Promise<string[]> {
    const options = this.page.locator(`#field-${name} option`);
    const count = await options.count();
    const values: string[] = [];
    for (let i = 0; i < count; i++) {
      const v = await options.nth(i).getAttribute("value");
      if (v) values.push(v);
    }
    return values;
  }

  /** Submit and wait for the save PATCH on the given entity, returning
   *  the response. After TKT-E6094 (autosave) edit forms no longer have
   *  an explicit Save button: the PATCH fires automatically after a
   *  debounce. We detect either mode and wait for the same PATCH.
   *
   *  For autosave forms, the caller should have just performed an edit
   *  (fillField/selectField etc.) — this helper triggers a blur to flush
   *  the debounce and then waits for the PATCH. */
  async saveAndWaitForPatch(plural: string, entityId: string) {
    const submitVisible = await this.submitButton
      .first()
      .waitFor({ state: "visible", timeout: 2000 })
      .then(() => true)
      .catch(() => false);
    if (submitVisible) {
      const [resp] = await Promise.all([
        this.page.waitForResponse(
          (r) =>
            r.url().includes(`/api/v1/${plural}/${entityId}`) &&
            r.request().method() === "PATCH",
        ),
        this.submitButton.first().click(),
      ]);
      return resp;
    }
    // Autosave path: blur to flush pending input, then wait for the
    // PATCH chain to drain. Multiple edits in quick succession serialize
    // through the FIFO queue, so we wait for an idle window of no PATCH
    // activity. Bounded so a no-edit form doesn't hang.
    await this.page.evaluate(() =>
      (document.activeElement as HTMLElement | null)?.blur(),
    );
    let lastResp:
      Awaited<ReturnType<typeof this.page.waitForResponse>> | undefined;
    // Poll for PATCH responses until 1s elapses without one.
    for (;;) {
      const resp = await this.page
        .waitForResponse(
          (r) =>
            r.url().includes(`/api/v1/${plural}/${entityId}`) &&
            r.request().method() === "PATCH",
          { timeout: 1500 },
        )
        .catch(() => undefined);
      if (!resp) break;
      lastResp = resp;
    }
    return lastResp;
  }

  /** True if the page shows an inline-create UI for related entities. */
  // --- Inline entity creation (TKT-OMUD56) ---
  //
  // These previously matched loose selectors that hit nothing, so the only
  // inline-create test was vacuous. They now target the real markup.

  /** The "+ New <Label>" buttons inside a relation widget. */
  inlineCreateButtons(scope?: Locator): Locator {
    return (scope ?? this.page).locator(".add-new-btn");
  }

  /** Open the inline-create modal for a target type. */
  async clickInlineCreate(label: string, scope?: Locator) {
    await this.inlineCreateButtons(scope)
      .filter({ hasText: `+ New ${label}` })
      .click();
  }

  /** The inline-create dialog. */
  get inlineCreateModal(): Locator {
    return this.page.locator(".inline-create-modal");
  }

  async expectInlineFormVisible() {
    await expect(this.inlineCreateModal).toBeVisible();
    // The nested form, not just the shell.
    await expect(this.inlineCreateModal.locator("form")).toBeVisible();
  }

  /** Fill a field inside the inline-create modal (ids are shared with the page form). */
  async fillInlineField(property: string, value: string) {
    await this.inlineCreateModal.locator(`#field-${property}`).fill(value);
  }

  /** A field inside the inline-create modal, for visibility assertions. */
  inlineField(property: string): Locator {
    return this.inlineCreateModal.locator(`#field-${property}`);
  }

  /** Submit the nested form. */
  async submitInlineForm() {
    await this.inlineCreateModal.locator('button[type="submit"]').click();
  }

  /** Cancel the nested form via its Cancel button. */
  async cancelInlineForm() {
    await this.inlineCreateModal.locator('button').filter({ hasText: "Cancel" }).click();
  }

  /** Open a relation picker's dropdown by clicking its search combobox. */
  async openRelationPicker(picker: Locator) {
    await picker.locator('input[role="combobox"]').click();
  }

  /** Selected-entity tiles inside a relation picker. */
  pickerSelections(picker: Locator): Locator {
    return picker.locator(".selected-entity");
  }

  /** A cards relation widget, scoped by its section label.
   *
   *  Scoping on the label (rather than any text in the widget) matters: a
   *  sibling widget's search placeholder can contain the same word. */
  relationCardsByLabel(label: string): Locator {
    return this.page
      .locator(".relation-cards")
      .filter({ has: this.page.locator(`.section-label:has-text("${label}")`) });
  }

  /** Open a cards widget's add panel. */
  async openCardsAddPanel(cards: Locator) {
    await cards.locator(".add-btn").click();
  }

  /** The pending link form shown once a cards target is selected. */
  cardsPendingLink(cards: Locator): Locator {
    return cards.locator(".new-relation-form");
  }

  /** The "Created <title> — link it below" notice in a cards widget. */
  cardsCreatedNotice(cards: Locator): Locator {
    return cards.locator(".created-notice");
  }

  /** A top-level form field, for asserting the host draft survived. */
  field(property: string): Locator {
    return this.page.locator(`#field-${property}`);
  }

  // --- Markdown body editor (EasyMDE) ---

  get contentField(): Locator {
    return this.page.locator(".content-field");
  }

  /** The editor shell: toolbar plus editable surface. */
  get markdownEditorShell(): Locator {
    return this.page.locator(".milkdown-editor-shell");
  }

  get markdownEditorRoot(): Locator {
    return this.page.locator(".milkdown-editor");
  }

  /** The editable surface ProseMirror renders into. */
  get proseMirror(): Locator {
    return this.markdownEditorRoot.locator(".ProseMirror");
  }

  async expectContentFieldVisible() {
    await expect(this.contentField).toBeVisible();
  }

  async expectContentLabelHasText(text: string) {
    await expect(this.contentField.locator("label")).toHaveText(text);
  }

  get markdownToolbar(): Locator {
    return this.markdownEditorShell.locator(".editor-toolbar");
  }

  /**
   * How many inline SVG icons the editor toolbar renders.
   *
   * The toolbar draws its glyphs as real `<svg>` elements rather than an icon
   * font, which is what lets the editor ship no font at all. A count of zero
   * means the icons regressed to something font-based.
   */
  async countToolbarSvgIcons(): Promise<number> {
    return await this.markdownEditorShell.locator("button svg").count();
  }

  async expectMarkdownEditorReady() {
    await expect(this.markdownEditorRoot).toBeVisible();
    await expect(this.markdownToolbar).toBeVisible();
    await expect(this.proseMirror).toBeVisible();
  }

  async typeMarkdownBody(text: string) {
    await this.proseMirror.click();
    await this.page.keyboard.type(text);
  }

  // ── Links in the markdown body ──────────────────────────────────────────

  /** A toolbar button by its accessible name. */
  markdownToolbarButton(name: string): Locator {
    return this.markdownToolbar.getByRole("button", { name, exact: true });
  }

  /** The modal that collects a link's address (and text, from a bare caret). */
  get linkDialog(): Locator {
    return this.page.locator(".link-dialog");
  }

  /** The validation message under the address field. */
  get linkDialogError(): Locator {
    return this.linkDialog.locator("#link-dialog-error");
  }

  /** The floating panel shown on the link under the caret or pointer. */
  get linkPanel(): Locator {
    return this.page.locator(".link-tooltip");
  }

  /** Anchors inside the editing surface. */
  get editorLinks(): Locator {
    return this.proseMirror.locator("a");
  }

  /** Horizontal rules inside the editing surface. */
  get editorDividers(): Locator {
    return this.proseMirror.locator("hr");
  }

  /**
   * Selects a word in the editing surface, as a drag over it would.
   *
   * Playwright has no "select this text" primitive for contenteditable, so
   * this walks the text nodes and sets a DOM range. Throws rather than
   * silently selecting nothing, which would make a later assertion fail
   * somewhere unrelated.
   */
  async selectWordInMarkdownBody(word: string) {
    await this.proseMirror.evaluate((surface, needle) => {
      const walker = document.createTreeWalker(surface, NodeFilter.SHOW_TEXT);
      while (walker.nextNode()) {
        const node = walker.currentNode as Text;
        const index = node.textContent?.indexOf(needle) ?? -1;
        if (index < 0) continue;
        const range = document.createRange();
        range.setStart(node, index);
        range.setEnd(node, index + needle.length);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
        (surface as HTMLElement).focus();
        return;
      }
      throw new Error(`text not found in editor: ${needle}`);
    }, word);
  }

  /**
   * Puts a collapsed caret inside a word, without selecting it.
   *
   * Distinct from `selectWordInMarkdownBody`: surfaces driven by caret position
   * behave differently from ones driven by a selection, and the link panel is
   * the former.
   */
  async placeCaretInMarkdownWord(word: string) {
    await this.proseMirror.evaluate((surface, needle) => {
      const walker = document.createTreeWalker(surface, NodeFilter.SHOW_TEXT);
      while (walker.nextNode()) {
        const node = walker.currentNode as Text;
        const index = node.textContent?.indexOf(needle) ?? -1;
        if (index < 0) continue;
        const at = index + Math.floor(needle.length / 2);
        const range = document.createRange();
        range.setStart(node, at);
        range.collapse(true);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
        (surface as HTMLElement).focus();
        return;
      }
      throw new Error(`text not found in editor: ${needle}`);
    }, word);
  }

  /** Opens the link dialog from the toolbar. */
  async openLinkDialog() {
    await this.markdownToolbarButton("Link").click();
  }

  /** Fills the address and confirms. The confirm label differs when editing. */
  async submitLinkDialog(url: string) {
    await expect(this.linkDialog).toBeVisible();
    await this.linkDialog.locator("input").last().fill(url);
    await this.linkDialog
      .getByRole("button", { name: /insert|save/i })
      .click();
  }

  /**
   * Bounding boxes of the first editor link and the floating panel.
   *
   * Exposed as geometry because that is the whole point of the test that uses
   * it: the panel used to anchor to the selection rather than the link, which
   * no unit test can see (happy-dom has no layout engine and returns all-zero
   * rects).
   */
  async linkAndPanelBoxes(): Promise<{
    link: { x: number; y: number; height: number };
    panel: { x: number; y: number };
  }> {
    const link = await this.editorLinks.first().boundingBox();
    const panel = await this.linkPanel.boundingBox();
    if (!link || !panel) {
      throw new Error("expected both the link and the panel to have a box");
    }
    return { link, panel };
  }

  /** Computed `font-family` of the bold toolbar button's `::before` pseudo
   *  element. Used by markdown-editor.spec.ts to assert that the bundled
   *  Font Awesome stylesheet actually applied (TKT-ZDRS) — EasyMDE renders
   *  its icons via FA glyph classes, so a non-FA family means the bundle
   *  failed to land. Returns null if the button isn't in the DOM (the
   *  caller asserts that separately, so failure messages stay diagnostic).
   *  Implemented via `evaluate` rather than a Locator because computed-style
   *  on a pseudo-element isn't exposed through Playwright's selector API. */
  async getBoldToolbarIconFontFamily(): Promise<string | null> {
    return this.markdownToolbar
      .locator(".fa-bold")
      .first()
      .evaluate((el) => window.getComputedStyle(el, "::before").fontFamily)
      .catch(() => null);
  }

  // --- Entity-reference picker (TKT-I5NO) ---

  /** Toolbar button that opens the EntityPickerModal. Located by its
   *  title attribute rather than the icon class so the test survives
   *  icon-swap refactors. */
  get insertEntityRefButton(): Locator {
    return this.markdownToolbar.locator(
      'button[title="Insert entity reference"]',
    );
  }

  /** Picker modal overlay. Teleported to <body>, so we query the page
   *  rather than the form root. */
  get entityPickerOverlay(): Locator {
    return this.page.locator(".entity-picker-overlay");
  }

  get entityPickerInput(): Locator {
    return this.entityPickerOverlay.locator(".entity-picker-input");
  }

  get entityPickerOptions(): Locator {
    return this.entityPickerOverlay.locator(".entity-picker-option");
  }

  /** Click the toolbar button and wait for the modal to render. */
  async openEntityPicker(): Promise<void> {
    await this.insertEntityRefButton.click();
    await expect(this.entityPickerInput).toBeFocused();
  }

  /** Type a query into the picker and wait for results to render. The
   *  150ms client debounce plus Bleve commit latency means we wait up to
   *  five seconds before failing — generous enough that a freshly-created
   *  entity surfaces, tight enough that a real bug still fails fast. */
  async searchEntityPicker(query: string): Promise<void> {
    await this.entityPickerInput.fill(query);
    await expect(this.entityPickerOptions.first()).toBeVisible({
      timeout: 5_000,
    });
  }

  /** The markdown the editor currently holds.
   *
   *  Read through the test hook the editor publishes rather than off an
   *  editor-instance property: the markdown is a SERIALIZATION of the
   *  ProseMirror document, not a buffer sitting on the DOM node, so there is
   *  nothing equivalent to CodeMirror's `getValue` to reach for. The hook is
   *  compiled out of production builds by `__E2E_TEST_HOOKS__`. */
  async getMarkdownBody(): Promise<string> {
    return await this.markdownEditorRoot.evaluate((el) => {
      const hook = (el as HTMLElement & {
        __relaGetMarkdown?: () => string;
      }).__relaGetMarkdown;
      return typeof hook === "function" ? hook() : "";
    });
  }

  // --- Inline `@` mention autocomplete ---

  /** The completion menu the editor opens on `@`.
   *
   *  Positioned by Milkdown's SlashProvider, which appends it to the editor's
   *  parent and toggles `data-show` rather than unmounting it — so visibility,
   *  not presence, is what the assertions key on. */
  get mentionMenu(): Locator {
    return this.markdownEditorShell.locator(".mention-menu");
  }

  /** Every row, across both sections. Prefer a section-scoped locator below
   *  when a test cares which kind of row it is acting on. */
  get mentionMenuOptions(): Locator {
    return this.mentionMenu.locator(".mention-menu-item");
  }

  // The two sections are `role="group"` inside ONE listbox (the highlight is a
  // single sequence across both). Addressed by `data-section` rather than by
  // their ARIA label, which varies: the Entities heading only renders when type
  // rows sit above it, so the group falls back to an aria-label without one.

  /** The type-picker rows, which scope the search rather than inserting. */
  get mentionMenuTypeOptions(): Locator {
    return this.mentionMenu.locator(
      "ul[data-section='types'] .mention-menu-item",
    );
  }

  /** The entity rows, which insert a reference. */
  get mentionMenuEntityOptions(): Locator {
    return this.mentionMenu.locator(
      "ul[data-section='entities'] .mention-menu-item",
    );
  }

  /** The row currently highlighted, across both sections. */
  get mentionMenuHighlighted(): Locator {
    return this.mentionMenu.locator(".mention-menu-item.is-highlighted");
  }

  /** The chip in the menu naming the type the search is scoped to. */
  get mentionMenuScopeChip(): Locator {
    return this.mentionMenu.locator(".mention-menu-chip");
  }

  /** The `@type:` scope as drawn in the document while the menu owns it. */
  get editorScopeChip(): Locator {
    return this.proseMirror.locator(".mention-scope-chip");
  }

  get mentionMenuNote(): Locator {
    return this.mentionMenu.locator(".mention-menu-note");
  }

  /** Entity references rendered as links inside the editor. */
  get editorEntityRefs(): Locator {
    return this.proseMirror.locator("a[data-entity-ref]");
  }

  // --- GFM task lists (BUG-KHQXHH) ---

  /** Task list items in the editor, as rendered by the task-list node view. */
  get editorTaskItems(): Locator {
    return this.proseMirror.locator("li[data-item-type='task']");
  }

  /** The checkboxes inside those items.
   *
   *  Deliberately `input[type=checkbox]` rather than the `data-checked`
   *  attribute: that is the selector `styles/markdown-content.css` uses, so
   *  asserting on it is what ties the editor's DOM to the rendered view. */
  get editorTaskCheckboxes(): Locator {
    return this.editorTaskItems.locator("input[type='checkbox']");
  }

  /** Checked state of every task item, in document order. */
  async editorTaskCheckedStates(): Promise<boolean[]> {
    return await this.editorTaskCheckboxes.evaluateAll((els) =>
      els.map((el) => (el as HTMLInputElement).checked),
    );
  }

  /** The computed `list-style-type` of the nth task item.
   *
   *  `none` proves the shared sheet's `:has(> input[type=checkbox])` rule
   *  matched. jsdom cannot evaluate `:has()`, so this can only be checked in a
   *  real browser. */
  async editorTaskListStyle(index: number): Promise<string> {
    return await this.editorTaskItems
      .nth(index)
      .evaluate((el) => getComputedStyle(el).listStyleType);
  }

  /** Bounding boxes of the nth task item's checkbox and its content wrapper,
   *  for asserting the two share a visual row. */
  async editorTaskRowBoxes(index: number) {
    const item = this.editorTaskItems.nth(index);
    return {
      checkbox: await item.locator("input[type='checkbox']").boundingBox(),
      content: await item.locator("div").first().boundingBox(),
    };
  }

  /** Click the nth task checkbox, as a user would. */
  async clickEditorTaskCheckbox(index: number): Promise<void> {
    await this.editorTaskCheckboxes.nth(index).click();
  }

  /** Focus the nth task checkbox and activate it with the keyboard. */
  async pressSpaceOnEditorTaskCheckbox(index: number): Promise<void> {
    const box = this.editorTaskCheckboxes.nth(index);
    await box.focus();
    await box.press(" ");
  }

  /** Computed bottom margin of the paragraph inside the nth task item.
   *
   *  The shared sheet gives every `<p>` a 14px bottom margin, which inside a
   *  task item's content wrapper becomes a blank line under each row. The
   *  rendered view avoids it because marked puts the text directly in the
   *  `<li>`; the editor needs one rule to match. */
  async editorTaskParagraphMarginBottom(index: number): Promise<string> {
    return await this.editorTaskItems
      .nth(index)
      .locator("div > p")
      .first()
      .evaluate((el) => getComputedStyle(el).marginBottom);
  }

  /** Type into the editor at the current cursor.
   *
   *  Plain `keyboard.type` — a contenteditable receives real key events, so
   *  none of the execCommand indirection the CodeMirror editor needed applies.
   *  This also means the input rules and the mention trigger see exactly what
   *  a user's typing produces. */
  async typeIntoEditor(text: string): Promise<void> {
    await this.proseMirror.click();
    await this.page.keyboard.type(text);
  }

  /** Clear the editor entirely. Useful when a form preloads a template body
   *  and the test wants a known starting state. */
  async clearEditorBuffer(): Promise<void> {
    await this.proseMirror.click();
    await this.page.keyboard.press("ControlOrMeta+a");
    await this.page.keyboard.press("Backspace");
  }

  /** Wait for the `@` completion menu to appear. */
  async waitForMentionMenu(): Promise<void> {
    await this.mentionMenu.waitFor({ state: "visible", timeout: 3_000 });
  }

  /** Wait until the editor's text contains `text`.
   *
   *  `typeIntoEditor` clicks and types without waiting for focus to settle, so
   *  under parallel load a keystroke can land before ProseMirror is listening
   *  and the tail of the string is dropped. A test that asserts on menu state
   *  right after typing then fails on a half-typed query. Gate on the text
   *  being present rather than assuming the type succeeded. */
  async expectEditorText(text: string): Promise<void> {
    await expect(this.proseMirror).toContainText(text, { timeout: 5_000 });
  }

  // --- Template selector ---

  get templateSelector(): Locator {
    return this.page.locator(".template-selector");
  }

  get templatePills(): Locator {
    return this.page.locator(".template-pill");
  }

  async templatePillCount(): Promise<number> {
    return this.templatePills.count();
  }

  async templateSelectorVisible(): Promise<boolean> {
    return this.templateSelector.isVisible().catch(() => false);
  }

  async clickTemplatePill(index: number) {
    await this.templatePills.nth(index).click();
  }

  async expectTemplatePillActive(index: number) {
    await expect(this.templatePills.nth(index)).toHaveClass(/active/);
  }

  // --- Relation picker (non-cards widget) ---

  /** Locate a non-cards relation picker by its rendered section label. The
   *  picker renders as `.form-field.relation-picker` with the label at the
   *  top; we filter on the label text. */
  relationPickerByLabel(label: string): Locator {
    return this.page
      .locator(".form-field.relation-picker", { hasText: label })
      .first();
  }

  /** A selected-entity tile inside a relation picker. The tile renders the
   *  entity's title (via `getEntityLabel`), so callers pass the title text. */
  pickerTileByText(picker: Locator, text: string): Locator {
    return picker.locator(".selected-entity", { hasText: text });
  }

  /** Type into the picker's combobox input and click the dropdown item
   *  that contains `optionText`. Picker-scoped on both sides — the input
   *  and the option are looked up inside `picker` — so it stays correct
   *  when the form has multiple pickers open. Cards-widget callers want
   *  `RelationCardsPage.linkTargetByIdWithSearch` instead; that path
   *  surfaces a different (search-then-meta-form) UI. */
  async pickInRelationPicker(
    picker: Locator,
    query: string,
    optionText: string,
  ) {
    const search = picker.locator('input[role="combobox"]');
    await expect(search).toBeVisible();
    await search.fill(query);
    const option = picker
      .locator(".dropdown-item", { hasText: optionText })
      .first();
    await expect(option).toBeVisible();
    await option.click();
  }

  /** Remove a selected entity tile from a relation picker. */
  async removePickerTile(picker: Locator, tileText: string) {
    await this.pickerTileByText(picker, tileText)
      .locator(".remove-btn")
      .click();
  }

  async saveAndWaitForNavigation() {
    await this.submitFormAndWaitForNavigation(this.submitButton.first());
  }

  // --- Manual ID / prefix picker (TKT-E7NNM) ---

  get idInput(): Locator {
    return this.page.locator('.id-field input[type="text"]');
  }

  get prefixSelect(): Locator {
    return this.page.locator(".id-field select");
  }

  async expectIdInputVisible() {
    await expect(this.idInput).toBeVisible();
  }

  async expectPrefixSelectVisible() {
    await expect(this.prefixSelect).toBeVisible();
  }

  async expectPrefixSelectHidden() {
    await expect(this.prefixSelect).toHaveCount(0);
  }

  async fillId(value: string) {
    await this.idInput.fill(value);
  }

  async selectPrefix(value: string) {
    await this.prefixSelect.selectOption(value);
  }

  async getPrefixOptions(): Promise<string[]> {
    return this.prefixSelect.locator("option").allTextContents();
  }

  // --- File attachments (TKT-7K3BJF) ---
  //
  // The file widget renders one `.file-widget` per `file`-type property,
  // inside that property's field shell. Scoping by the field id keeps these
  // helpers usable on a form with several file properties.

  private fileWidget(property: string): Locator {
    // FieldRenderer passes `id="field-<property>"` to the widget, and
    // FileWidget binds it to its root — the same convention `#field-title`
    // already relies on.
    return this.page.locator(`#field-${property}.file-widget`);
  }

  /** The hidden `<input type=file>` behind the widget's pick control. */
  private fileInput(property: string): Locator {
    return this.fileWidget(property).locator('input[type="file"]');
  }

  /** Wait for a `file` property's widget to render.
   *
   *  Necessary because a create form does not render its fields until the
   *  staged-affordance dry-run POST resolves (`affordanceVisible` gates on
   *  `stagedAffordancesReady`), and `clickCreateButton` only waits for
   *  `domcontentloaded`. On an unloaded machine the dry-run lands first and
   *  the widget is there immediately; under parallel load it does not, so
   *  asserting on the widget without this wait is a race that only shows up
   *  in a full-suite run. */
  async waitForFileWidget(property: string) {
    await expect(this.fileWidget(property)).toBeVisible();
  }

  /** Attach a file to a `file` property. In create mode this only STAGES it
   *  (nothing is uploaded until the entity exists); in edit mode it uploads
   *  immediately. `name` is the filename the server will see. */
  async attachFile(property: string, name: string, contents: string, mimeType = "text/plain") {
    await this.waitForFileWidget(property);
    await this.fileInput(property).setInputFiles({
      name,
      mimeType,
      buffer: Buffer.from(contents),
    });
  }

  /** Filenames currently listed on a `file` property — staged or uploaded. */
  async attachedFileNames(property: string): Promise<string[]> {
    await this.waitForFileWidget(property);
    return this.fileWidget(property).locator(".file-name").allInnerTexts();
  }

  /** Remove the file at `index` from a `file` property's list. */
  async removeAttachedFile(property: string, index = 0) {
    await this.fileWidget(property).locator(".file-remove").nth(index).click();
  }

  /** Whether the add/replace control is offered for a `file` property.
   *  False at capacity, or when the widget cannot mutate. */
  async canAddFile(property: string): Promise<boolean> {
    await this.waitForFileWidget(property);
    return (await this.fileWidget(property).locator(".file-dropzone").count()) > 0;
  }

  /** The widget's inline error text, or '' when none is shown. */
  async fileErrorText(property: string): Promise<string> {
    const err = this.fileWidget(property).locator(".file-error");
    return (await err.count()) > 0 ? err.innerText() : "";
  }

  /** The inline validation error rendered under a field, or '' when none.
   *  FieldShell renders `.field-error` for every field type, file included. */
  async fieldErrorText(property: string): Promise<string> {
    const err = this.page.locator(`#field-${property}`).locator("xpath=..").locator(".field-error");
    return (await err.count()) > 0 ? err.first().innerText() : "";
  }

  /** Click Create and assert NO create request is issued — the client-side
   *  required gate should stop it. Waits a beat so a POST that was going to
   *  fire has time to. */
  async submitExpectingNoCreate(plural: string) {
    let posted = false;
    const listener = (r: { url: () => string; method: () => string }) => {
      // `?dry_run=true` POSTs the same path on every keystroke to re-derive
      // create-mode affordances — it persists nothing, so it must not count
      // as a create here.
      const url = r.url();
      if (url.includes(`/api/v1/${plural}`) && !url.includes("dry_run") && r.method() === "POST") {
        posted = true;
      }
    };
    this.page.on("request", listener);
    await this.submitButton.first().click();
    await this.page.waitForTimeout(500);
    this.page.off("request", listener);
    expect(posted, "a blocked submit must not POST").toBe(false);
  }

  /** Text of the error toast, waited for. Used by the create-with-attachments
   *  failure path, where the entity IS created but an upload was rejected —
   *  the message is the only thing telling the user which files to re-attach. */
  async errorToastText(): Promise<string> {
    const toast = this.toastContainer.first();
    await expect(toast).toBeVisible();
    return toast.innerText();
  }

  /** Submit a create that is expected to also upload `expectedUploads`
   *  attachments, waiting for the POST and every attachment PUT so the
   *  assertions that follow don't race the second phase. */
  async submitAndExpectCreateWithUploads(
    plural: string,
    expectedUploads: number,
  ): Promise<{ id: string }> {
    const uploads = Array.from({ length: expectedUploads }, () =>
      this.page.waitForResponse(
        (r) =>
          r.url().includes("/_attachments/") && r.request().method() === "PUT",
      ),
    );
    const created = await this.submitAndExpectCreate(plural);
    await Promise.all(uploads);
    return created;
  }
}
