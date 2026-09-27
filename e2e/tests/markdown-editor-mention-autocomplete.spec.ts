import { test, expect } from './fixtures';
import { FormPage, EntityPage } from '../pages';

// Verifies the markdown editor's inline `@`-triggered entity-reference
// completion menu (TKT-39TIB4). Complements the modal picker by covering the
// keyboard-driven inline path.
//
// A bare `@` shows a starting list; one or two letters offer types; longer
// queries search. Choosing a type writes `@type:` into the document, which
// scopes the search until the `:` is deleted.
//
// Only the first `typeIntoEditor` in a test clicks: a click moves the cursor,
// and moving the cursor releases the `@` for good. Later typing goes through
// `appPage.keyboard.type`.
test.describe('Markdown editor @ mention autocomplete', () => {
  let targetId: string | null = null;
  let originId: string | null = null;
  const targetTitleToken = 'mentionz' + Math.random().toString(36).slice(2, 8);
  const targetTitle = `${targetTitleToken} Target Feature`;

  test.beforeEach(async ({ api }) => {
    const target = await api.createEntity('features', {
      properties: {
        title: targetTitle,
        description: 'Selected via the inline @ completion menu',
        status: 'draft',
        priority: 'medium',
      },
    });
    targetId = target.id;
    // The menu hits /_search, backed by a Bleve index that commits
    // asynchronously after creates.
    await api.waitForIndexed(target.id);
  });

  test.afterEach(async ({ api }) => {
    if (originId) {
      await api.deleteEntity('features', originId).catch(() => {});
      originId = null;
    }
    if (targetId) {
      await api.deleteEntity('features', targetId).catch(() => {});
      targetId = null;
    }
  });

  test('a bare @ shows a starting list of entities, not types', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @');
    await form.waitForMentionMenu();
    await expect(form.mentionMenuEntityOptions.first()).toBeVisible({ timeout: 5_000 });
    await expect(form.mentionMenuTypeOptions).toHaveCount(0);
  });

  test('one letter offers only the types it starts', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @f');
    await form.expectEditorText('see @f');
    await form.waitForMentionMenu();
    // Assert on CONTENT, which `expect` retries; a count read once can see the
    // list from before Vue re-rendered.
    await expect(form.mentionMenuTypeOptions.filter({ hasText: 'feature' })).toHaveCount(1);
    await expect(form.mentionMenuTypeOptions.filter({ hasText: 'bug' })).toHaveCount(0);
    await expect(form.mentionMenuEntityOptions).toHaveCount(0);
  });

  test('the arrow keys move the highlight, one row at a time', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @');
    await form.waitForMentionMenu();
    await expect.poll(() => form.mentionMenuOptions.count(), { timeout: 5_000 }).toBeGreaterThan(1);

    const highlighted = form.mentionMenuHighlighted;
    // Exactly one row is highlighted at every point, and it actually moves.
    await expect(highlighted).toHaveCount(1);
    const first = await highlighted.innerText();

    await appPage.keyboard.press('ArrowDown');
    await expect(highlighted).toHaveCount(1);
    expect(await highlighted.innerText()).not.toBe(first);

    await appPage.keyboard.press('ArrowUp');
    await expect(highlighted).toHaveCount(1);
    expect(await highlighted.innerText()).toBe(first);
  });

  // Focus stays in the editor, so the rows are never focused and the ONLY way a
  // screen reader learns which row is active is `aria-activedescendant` naming
  // it. A dangling idref reads as nothing at all, so this asserts the reference
  // actually resolves to the highlighted element rather than merely being set.
  test('the listbox names the active row for assistive tech', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @');
    await form.waitForMentionMenu();
    await expect.poll(() => form.mentionMenuOptions.count(), { timeout: 5_000 }).toBeGreaterThan(1);

    const probe = () =>
      appPage.evaluate(() => {
        const box = document.querySelector('.mention-menu[role="listbox"]');
        const id = box?.getAttribute('aria-activedescendant') ?? null;
        // Resolve WITHIN this listbox: useId() ids are document-scoped, so a
        // page with two editors could otherwise satisfy the lookup wrongly.
        const target = id ? box?.querySelector(`#${CSS.escape(id)}`) : null;
        return {
          nested: document.querySelectorAll('.mention-menu [role="listbox"]').length,
          resolves: target !== null,
          onHighlighted: target?.classList.contains('is-highlighted') ?? false,
          id,
        };
      });

    const before = await probe();
    expect(before.nested).toBe(0);
    expect(before.resolves).toBe(true);
    expect(before.onHighlighted).toBe(true);

    await appPage.keyboard.press('ArrowDown');
    const after = await probe();
    expect(after.resolves).toBe(true);
    expect(after.onHighlighted).toBe(true);
    expect(after.id).not.toBe(before.id);
  });

  test('typing a query lists the matching entity first', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    await form.waitForMentionMenu();
    await expect(form.mentionMenuEntityOptions.first()).toContainText(targetId!, {
      timeout: 5_000,
    });
    await expect(form.mentionMenuHighlighted).toContainText(targetId!);
  });

  test('choosing a type writes the scope into the document', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @fe');
    await form.expectEditorText('see @fe');
    await form.waitForMentionMenu();
    await form.mentionMenuTypeOptions.filter({ hasText: 'feature' }).first().click();

    // The letters typed to find the type are consumed (spec 4.1).
    await form.expectEditorText('see @feature:');
    await expect(form.editorScopeChip).toHaveText('@feature:');
    await expect(form.mentionMenuScopeChip).toHaveText('feature');
    await expect(form.mentionMenuTypeOptions).toHaveCount(0);

    await appPage.keyboard.type(targetTitleToken);
    await expect(form.mentionMenuEntityOptions.first()).toContainText(targetId!, {
      timeout: 5_000,
    });
    await appPage.keyboard.press('Enter');
    await expect(form.mentionMenu).not.toBeVisible();
    const body = await form.getMarkdownBody();
    expect(body).toContain(`\`${targetId}\``);
    expect(body).not.toContain('@feature:');
  });

  // Back-to-back with no waits: the scope is document text, so deleting the
  // `:` must unscope on the very next update.
  test('Backspace over the colon unscopes', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @feature:');
    await form.expectEditorText('see @feature:');
    await expect(form.editorScopeChip).toHaveText('@feature:');
    await expect(form.mentionMenuScopeChip).toHaveText('feature');

    await appPage.keyboard.press('Backspace');
    await form.expectEditorText('see @feature');
    await expect(form.editorScopeChip).toHaveCount(0);
    await expect(form.mentionMenuScopeChip).toHaveCount(0);
    await expect(form.mentionMenuTypeOptions.filter({ hasText: 'feature' })).toHaveCount(1);
  });

  test('Enter pressed before the results land inserts the top hit', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    // No wait for the rows: the search is still debouncing.
    await appPage.keyboard.press('Enter');
    await expect(form.editorEntityRefs.first()).toHaveAttribute('data-entity-ref', targetId!, {
      timeout: 5_000,
    });
  });

  test('Enter inserts the reference and closes the menu', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    await form.waitForMentionMenu();
    await expect(form.mentionMenuEntityOptions.first()).toBeVisible({ timeout: 10_000 });
    await appPage.keyboard.press('Enter');

    await expect(form.mentionMenu).not.toBeVisible();
    // The trigger and the query are both consumed: only the reference is
    // left, serialized as the code span the store holds.
    const body = await form.getMarkdownBody();
    expect(body).toContain(`\`${targetId}\``);
    expect(body).not.toContain('@' + targetTitleToken);
  });

  test('the inserted reference renders as a titled link inside the editor', async ({
    appPage,
  }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    await form.waitForMentionMenu();
    await expect(form.mentionMenuEntityOptions.first()).toBeVisible({ timeout: 10_000 });
    await appPage.keyboard.press('Enter');

    // The title the menu just showed is carried onto the node, so the
    // reference reads correctly straight away rather than waiting for a
    // mentions refresh that would not include a just-inserted ID.
    const ref = form.editorEntityRefs.first();
    await expect(ref).toBeVisible();
    await expect(ref).toHaveText(targetTitle);
    await expect(ref).toHaveAttribute('data-entity-ref', targetId!);
  });

  test('a query with no matches says so, then closes', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @qqzzxx');
    await form.waitForMentionMenu();
    await expect(form.mentionMenuNote).toHaveText('No matches', { timeout: 5_000 });
    await appPage.keyboard.type('qzx');
    await expect(form.mentionMenu).not.toBeVisible();
  });

  test('Escape closes the menu; editing the query reopens it', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    await form.waitForMentionMenu();
    await appPage.keyboard.press('Escape');
    await expect(form.mentionMenu).not.toBeVisible();
    const body = await form.getMarkdownBody();
    expect(body).not.toContain(`\`${targetId}\``);

    await appPage.keyboard.press('Backspace');
    await form.waitForMentionMenu();
  });

  test('moving the cursor back into an old query does not reopen the menu', async ({
    appPage,
  }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @abc done');
    await form.expectEditorText('see @abc done');
    await expect(form.mentionMenu).not.toBeVisible();
    for (let i = 0; i < ' done'.length + 1; i++) await appPage.keyboard.press('ArrowLeft');
    await appPage.keyboard.type('x');
    await form.expectEditorText('see @abxc done');
    await expect(form.mentionMenu).not.toBeVisible();
  });

  test('a space after the trigger closes the menu', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('see @ ');
    // Ordinary prose containing an `@` must not leave a menu hanging open.
    await expect(form.mentionMenu).not.toBeVisible({ timeout: 2_000 });
  });

  test('an email address does not trigger the menu', async ({ appPage }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.clearEditorBuffer();
    await form.typeIntoEditor('mail someone@example');
    await expect(form.mentionMenu).not.toBeVisible({ timeout: 2_000 });
  });

  test('round-trip: the saved body renders the rewritten link on the detail page', async ({
    appPage,
    api,
  }) => {
    const form = new FormPage(appPage);
    await form.navigateToCreateForm('feature');
    await form.expectMarkdownEditorReady();
    await form.fillField('title', 'Mention Origin Feature');
    await form.selectField('priority', 'low');
    await form.clearEditorBuffer();
    await form.typeIntoEditor(`see @${targetTitleToken}`);
    await form.waitForMentionMenu();
    await expect(form.mentionMenuEntityOptions.first()).toBeVisible({ timeout: 10_000 });
    await appPage.keyboard.press('Enter');

    const created = await form.submitAndExpectCreate('features');
    originId = created.id;

    const entity = new EntityPage(appPage);
    await entity.navigateToEntity('feature', originId!);
    const link = entity.contentEntityRefLink('feature', targetId!);
    await expect(link).toBeVisible();
    await expect(link).toHaveText(targetTitle);

    // The stored markdown is a plain code span, not a link: the title is a
    // per-principal view concern and must never reach the file.
    const persisted = await api.getContent('features', originId!);
    expect(persisted).toContain(`\`${targetId}\``);
    expect(persisted).not.toContain(targetTitle);
  });
});
