import { type Page, type Locator, expect } from '@playwright/test';
import { BasePage } from './base.page';

export class KanbanPage extends BasePage {
  readonly board: Locator;
  readonly columns: Locator;
  readonly cards: Locator;
  readonly createButton: Locator;
  readonly filterBar: Locator;

  constructor(page: Page) {
    super(page);
    this.board = page.locator('.kanban-board');
    this.columns = page.locator('.kanban-column');
    this.cards = page.locator('.kanban-card');
    this.createButton = page.locator('button:has-text("+ New"), button:has-text("New")');
    this.filterBar = page.locator('.filter-bar');
  }

  async navigateToKanban(kanbanId: string) {
    await this.navigateTo(`/kanban/${kanbanId}`);
    await this.waitForSpinnerToDisappear();
  }

  async getColumnCount(): Promise<number> {
    return this.columns.count();
  }

  async getCardCount(): Promise<number> {
    return this.cards.count();
  }

  async getColumnCardCount(columnName: string): Promise<number> {
    const column = this.columns.filter({ hasText: columnName });
    return column.locator('.kanban-card').count();
  }

  getColumn(name: string): Locator {
    return this.columns.filter({ has: this.page.locator('.column-title, .column-header').filter({ hasText: name }) });
  }

  async clickCard(cardTitle: string) {
    await this.cards.filter({ hasText: cardTitle }).click();
    await this.page.waitForURL(/\/entity\/|\/form\//);
  }

  /** Modifier-click a kanban card and return the tab it opens (TKT-3CSZRG). */
  async openCardInNewTab(cardId: string, modifier: 'ControlOrMeta' = 'ControlOrMeta') {
    const popupPromise = this.page.context().waitForEvent('page');
    await this.cards.filter({ hasText: cardId }).click({ modifiers: [modifier] });
    return popupPromise;
  }

  async clickCardById(cardId: string) {
    await this.cards.filter({ hasText: cardId }).click();
    await this.page.waitForURL(/\/entity\/|\/form\//);
  }

  /** Perform HTML5 drag-and-drop by dispatching native drag events and wait for
   *  the resulting PATCH response. Playwright's high-level dragTo doesn't always
   *  dispatch `drop` on Vue @drop listeners for native draggable elements. */
  private async dragCardToColumnLocator(card: Locator, columnCards: Locator) {
    const patchPromise = this.page.waitForResponse(
      r => /\/api\/v1\/[^/]+\/[^/]+$/.test(r.url()) && r.request().method() === 'PATCH',
      { timeout: 5000 },
    ).catch(() => null);
    await card.evaluate((sourceEl, targetSelector) => {
      const dt = new DataTransfer();
      sourceEl.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: dt }));
      const target = document.querySelector(targetSelector) as HTMLElement | null;
      if (!target) throw new Error(`drop target not found: ${targetSelector}`);
      target.dispatchEvent(new DragEvent('dragenter', { bubbles: true, dataTransfer: dt }));
      target.dispatchEvent(new DragEvent('dragover', { bubbles: true, dataTransfer: dt }));
      target.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: dt }));
      sourceEl.dispatchEvent(new DragEvent('dragend', { bubbles: true, dataTransfer: dt }));
    }, await columnCards.evaluate(el => {
      // Build a stable selector using id or a generated data attribute
      if (!el.id) el.id = `dnd-target-${Date.now()}-${Math.random().toString(36).slice(2)}`;
      return `#${el.id}`;
    }));
    await patchPromise;
  }

  async dragCardToColumn(cardTitle: string, targetColumnName: string) {
    const card = this.cards.filter({ hasText: cardTitle });
    const targetColumn = this.getColumn(targetColumnName);
    await this.dragCardToColumnLocator(card, targetColumn.locator('.column-cards'));
  }

  async dragCardByIdToColumn(cardId: string, targetColumnName: string) {
    const card = this.cards.filter({ hasText: cardId });
    const targetColumn = this.getColumn(targetColumnName);
    await this.dragCardToColumnLocator(card, targetColumn.locator('.column-cards'));
  }

  /** Pick a value in the filter control labelled `label` (the board renders
   *  the list's FilterBar, whose <label for> names each control). */
  async setFilter(label: string, value: string) {
    await this.filterBar.getByLabel(label, { exact: true }).selectOption(value);
    await this.waitForSpinnerToDisappear();
  }

  async clickCreate() {
    await this.createButton.click();
    await this.page.waitForLoadState('domcontentloaded');
  }

  async expectColumnCount(count: number) {
    await expect(this.columns).toHaveCount(count);
  }

  async expectColumnLabel(label: string) {
    await expect(this.page.locator('.column-title').filter({ hasText: label })).toBeVisible();
  }

  async expectFirstCardSeverityVisible() {
    await expect(this.cards.first().getByText(/high|critical|medium|low/)).toBeVisible();
  }

  async expectCardCount(count: number) {
    await expect(this.cards).toHaveCount(count);
  }

  async expectCardInColumn(cardTitle: string, columnName: string) {
    const column = this.getColumn(columnName);
    await expect(column.locator('.kanban-card').filter({ hasText: cardTitle })).toBeVisible();
  }

  async expectCardIdInColumn(cardId: string, columnName: string) {
    const column = this.getColumn(columnName);
    await expect(column.locator('.kanban-card').filter({ hasText: cardId })).toBeVisible();
  }

  async expectColumnCardCount(columnName: string, count: number) {
    const column = this.getColumn(columnName);
    const countBadge = column.locator('.column-count');
    await expect(countBadge).toHaveText(String(count));
  }

  async expectColumnCountVisible(columnName: string) {
    const column = this.getColumn(columnName);
    await expect(column.locator('.column-count')).toBeVisible();
  }

  async expectEmptyColumn(columnName: string) {
    const column = this.getColumn(columnName);
    await expect(column.locator('.empty-column')).toBeVisible();
  }
}
