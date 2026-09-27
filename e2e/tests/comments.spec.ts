import { test, expect } from './fixtures';
import { CommentsPage } from '../pages';

/**
 * Entity commenting (TKT-FIO205).
 *
 * The unit and Go tests cover the anchor algebra and the ACL; these cover what
 * only a real browser can: that a selection in RENDERED markdown resolves to
 * the right span of SOURCE markdown, that a highlight survives the round-trip
 * through the renderer and DOMPurify, and that the surfaces stay in agreement.
 */
test.describe('Comments', () => {
  // TASK-001 is seeded with a markdown body ("Write unit tests for auth
  // module."), and the task view renders it — a text anchor needs both.
  const TYPE = 'task';
  const ID = 'TASK-001';

  test.afterEach(async ({ api }) => {
    // Comments live outside the entity store, so removing them means deleting
    // each one; the entity itself is seed data other specs rely on.
    const res = await api.listComments(TYPE, ID).catch(() => null);
    for (const c of res?.comments ?? []) {
      await api.deleteComment(TYPE, ID, c.id).catch(() => {});
    }
    // Accepting a suggestion and moving text both edit the seeded body.
    await api.setEntityContent('tasks', ID, 'Write unit tests for auth module.\n');
  });

  test('comments on a property, and the indicator counts it', async ({ appPage }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    expect(await comments.fieldCommentCount('status')).toBeNull();

    await comments.openField('status');
    await comments.postFieldComment('Should this still be draft?');

    await expect(comments.fieldCommentBodies()).toHaveText([
      'Should this still be draft?',
    ]);
    expect(await comments.fieldCommentCount('status')).toBe('1');
  });

  test('comments on selected body text and highlights it', async ({ appPage }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.selectBodyText('unit tests for auth');
    await expect(comments.selectionButton()).toBeVisible();
    await comments.commentOnSelection('Which auth module?');

    // The highlight proves the whole round-trip: the rendered selection
    // resolved to a source range, and the mark survived the re-render and
    // DOMPurify's attribute allowlist.
    await expect(comments.highlights()).toHaveCount(1);
    await expect(comments.highlights().first()).toHaveText('unit tests for auth');
  });

  test('opens a highlight thread and accepts a reply', async ({ appPage }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.selectBodyText('unit tests for auth');
    await comments.commentOnSelection('First remark');

    await comments.openHighlight('unit tests for auth');
    await expect(comments.highlightCommentBodies()).toHaveText(['First remark']);

    // A reply shares the anchor, so the thread must show BOTH — the bug that
    // made a saved reply look lost was the popover showing only one.
    await comments.replyInThread('A reply on the same text');
    await expect(comments.highlightCommentBodies()).toHaveText([
      'First remark',
      'A reply on the same text',
    ]);
  });

  test('the catch-all panel counts comments with no field row', async ({ appPage }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.openField('status');
    await comments.postFieldComment('On a field');
    await comments.dismissPopover();

    await comments.selectBodyText('unit tests for auth');
    await comments.commentOnSelection('On the body');

    // A text anchor has no field row, so it is what the summary counts.
    await expect(comments.panelSummary()).toContainText('2 total');
    await expect(comments.panelSummary()).toContainText('1 not on a field');

    await comments.expandPanel();
    await expect(comments.panelComments()).toHaveCount(2);
  });

  test('deleting from a field popover updates the panel without a reload', async ({
    appPage,
  }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.openField('status');
    await comments.postFieldComment('Doomed comment');
    await comments.dismissPopover();

    await comments.expandPanel();
    await expect(comments.panelComments()).toHaveCount(1);

    // Regression: the panel used to fetch its own copy of the thread, so a
    // delete elsewhere left a stale row until the page was reloaded.
    await comments.openField('status');
    await comments.deleteFirstFieldComment();

    await expect(comments.panelComments()).toHaveCount(0);
  });

  test('survives an edit that moves the anchored text', async ({ appPage, api }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.selectBodyText('unit tests for auth');
    await comments.commentOnSelection('Still about the same words');

    // Insert a paragraph ABOVE the quote: every byte offset after it shifts,
    // which is precisely what a stored offset could not survive.
    await api.setEntityContent(
      'tasks',
      ID,
      'A paragraph added later, which shifts every offset below it.\n\n' +
        'Write unit tests for auth module.\n',
    );
    await comments.openEntity(TYPE, ID);

    await expect(comments.highlights()).toHaveCount(1);
    await expect(comments.highlights().first()).toHaveText('unit tests for auth');
  });

  test('suggests a replacement and accepts it into the body', async ({ appPage, api }) => {
    const comments = new CommentsPage(appPage);
    await comments.openEntity(TYPE, ID);

    await comments.selectBodyText('unit tests for auth');
    await comments.suggestOnSelection('integration tests for auth');

    await comments.openHighlight('unit tests for auth');
    await expect(comments.threadDiffRemoved()).toHaveText('unit tests for auth');
    await expect(comments.threadDiffAdded()).toHaveText('integration tests for auth');

    await comments.acceptInThread();

    // The body is rewritten on the server and shown without a reload, and
    // accepting resolves the suggestion.
    await expect(comments.entityBody()).toContainText(
      'Write integration tests for auth module.',
    );
    const res = await api.listComments(TYPE, ID);
    expect(res.comments.map((c) => c.resolved)).toEqual([true]);

    // A reload proves it was stored, not only patched into the view.
    await comments.openEntity(TYPE, ID);
    await expect(comments.entityBody()).toContainText(
      'Write integration tests for auth module.',
    );
  });
});
