import { test, expect } from './fixtures';
import { KanbanPage, FormPage } from '../pages';

test.describe('Kanban Board', () => {
  test.describe('Display', () => {
    test('displays kanban board with columns', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');
      await kanbanPage.expectHeading('Feature Board');

      // Should have 4 columns for feature status
      await kanbanPage.expectColumnCount(4);
    });

    test('shows correct column labels', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      // Check column headers
      await kanbanPage.expectColumnLabel('Draft');
      await kanbanPage.expectColumnLabel('Approved');
      await kanbanPage.expectColumnLabel('In Progress');
      await kanbanPage.expectColumnLabel('Done');
    });

    test('displays cards with correct content', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      // Cards should show title
      await expect(kanbanPage.cards.filter({ hasText: 'User Authentication' })).toBeVisible();
      await expect(kanbanPage.cards.filter({ hasText: 'Dashboard Analytics' })).toBeVisible();
    });

    test('shows card count per column', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');
      await kanbanPage.expectColumnCountVisible('Approved');
    });

    test('shows create button', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      await expect(kanbanPage.createButton).toBeVisible();
    });
  });

  test.describe('Card Interaction', () => {
    test('clicking card opens entity', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      await kanbanPage.clickCard('User Authentication');

      // Should navigate to form or entity view
      await expect(appPage).toHaveURL(/\/form\/|\/entity\//);
    });

    test('cards are in correct columns', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      // FEAT-001 has status 'approved'
      await kanbanPage.expectCardInColumn('User Authentication', 'Approved');

      // FEAT-002 has status 'draft'
      await kanbanPage.expectCardInColumn('Dashboard Analytics', 'Draft');

      // FEAT-003 has status 'in_progress'
      await kanbanPage.expectCardInColumn('Export Data', 'In Progress');
    });
  });

  test.describe('Drag and Drop', () => {
    test('can drag card to different column', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      // FEAT-002 is in Draft, drag to Approved
      await kanbanPage.dragCardToColumn('Dashboard Analytics', 'Approved');

      // Card should now be in Approved column
      await kanbanPage.expectCardInColumn('Dashboard Analytics', 'Approved');
    });

    test('drag updates the entity status', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);
      const formPage = new FormPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');
      await kanbanPage.dragCardToColumn('Dashboard Analytics', 'Done');
      await kanbanPage.clickCard('Dashboard Analytics');

      await formPage.expectFieldValue('status', 'done');
    });
  });

  test.describe('Filtering', () => {
    test('filter controls are visible when configured', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      // Feature board has filter_controls for priority
      await expect(kanbanPage.filterBar).toBeVisible();
    });

    test('can filter cards by property', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');
      await expect(kanbanPage.cards.filter({ hasText: 'Dashboard Analytics' })).toBeVisible();

      // Only FEAT-001 (User Authentication) has priority high.
      await kanbanPage.setFilter('Priority', 'high');

      await expect(kanbanPage.cards.filter({ hasText: 'User Authentication' })).toBeVisible();
      await expect(kanbanPage.cards.filter({ hasText: 'Dashboard Analytics' })).toHaveCount(0);
    });

    test('can filter cards by relation', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');
      await expect(kanbanPage.cards.filter({ hasText: 'Dashboard Analytics' })).toBeVisible();

      // Only FEAT-001 blocks FEAT-003 (Export Data). The control's options are
      // the relation's targets; the board used to ignore relation controls.
      await kanbanPage.setFilter('Blocks', 'Export Data');

      await expect(kanbanPage.cards.filter({ hasText: 'User Authentication' })).toBeVisible();
      await expect(kanbanPage.cards.filter({ hasText: 'Dashboard Analytics' })).toHaveCount(0);
      await expect(appPage).toHaveURL(/filter%5Bblocks%5D|filter\[blocks\]/);
    });
  });

  test.describe('Create from Kanban', () => {
    test('create button opens form', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      await kanbanPage.clickCreate();

      await expect(appPage).toHaveURL(/\/form\/feature/);
    });

    test('can create entity from kanban and see it on board', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);
      const formPage = new FormPage(appPage);

      await kanbanPage.navigateToKanban('feature-board');

      await kanbanPage.clickCreate();

      // Fill form
      await formPage.fillField('title', 'Kanban Created Feature');
      await formPage.selectField('status', 'approved');
      await formPage.submit();

      // Navigate back to kanban
      await kanbanPage.navigateToKanban('feature-board');

      // Should see the new card in Approved column
      await kanbanPage.expectCardInColumn('Kanban Created Feature', 'Approved');
    });
  });

  test.describe('Bug Board', () => {
    test('bug board displays correctly', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('bug-board');
      await kanbanPage.expectHeading('Bug Board');

      // Should have 3 columns
      await kanbanPage.expectColumnCount(3);

      // Check column labels
      await kanbanPage.expectColumnLabel('New');
      await kanbanPage.expectColumnLabel('In Progress');
      await kanbanPage.expectColumnLabel('Fixed');
    });

    test('bug cards show severity badge', async ({ appPage }) => {
      const kanbanPage = new KanbanPage(appPage);

      await kanbanPage.navigateToKanban('bug-board');

      // Cards should show severity
      await kanbanPage.expectFirstCardSeverityVisible();
    });
  });
});
