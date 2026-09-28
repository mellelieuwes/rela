<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useSchemaStore } from '@/stores'
import NextActionCard from '@/components/NextActionCard.vue'
import { searchEntities, analyze } from '@/api'
import { DEFAULT_WORLD } from '@/composables/useWorld'
import type { Entity, DashboardCard, AnalyzeResult } from '@/types'

const schemaStore = useSchemaStore()

// State
const loading = ref(true)

// pageState mirrors DynamicForm's `form-state-*` contract: a stable signal
// that this screen has finished resolving, so a screenshot{} capture can wait
// for it rather than hanging until its timeout.
const loadError = ref(false)
const pageState = computed<'pending' | 'loaded' | 'error'>(() => {
  if (loadError.value) return 'error'
  return loading.value ? 'pending' : 'loaded'
})
// Keyed by cardKey(), not by array index: the card list is per-principal
// (TKT-53KICM), so its length and contents can differ between loads. An
// index-keyed map survives such a change and binds one card's rows to
// another's tile — e.g. dropping the first of two cards leaves the survivor
// rendering the dropped card's count.
const cardData = ref<Map<string, { entities: Entity[]; count: number }>>(new Map())
const analysisResult = ref<AnalyzeResult | null>(null)

/**
 * A stable identity for a card, derived from its content.
 *
 * Cards have no configured id, so this is the best available substitute:
 * `title` alone is not guaranteed unique, and `query` alone is genuinely
 * shared by cards that display the same data differently.
 *
 * JSON.stringify rather than string concatenation, so the parts cannot run
 * together: `{title:'a b', query:'c'}` and `{title:'a', query:'b c'}` must not
 * collide into one key.
 */
function cardKey(card: DashboardCard): string {
  return JSON.stringify([card.title, card.query, card.display])
}

// Computed
const dashboardConfig = computed(() => schemaStore.dashboard)
const title = computed(() => dashboardConfig.value?.title || 'Dashboard')
const description = computed(() => dashboardConfig.value?.description)
const cards = computed(() => dashboardConfig.value?.cards || [])

// Methods
async function loadData() {
  loading.value = true
  loadError.value = false

  try {
    // Load card data in parallel
    const cardPromises = cards.value.map(async (card) => {
      // Cards count in the default world, as they did before `_search`
      // honoured `app.default_world` (BUG-SMPOZB). Counting a card in a
      // publication world is a separate decision, like next-action's
      // per-source `source_world:`.
      const response = await searchEntities(card.query, undefined, undefined, DEFAULT_WORLD)
      cardData.value.set(cardKey(card), {
        entities: response.data,
        count: response.meta.total,
      })
    })

    // Load analysis
    const analysisPromise = analyze()

    await Promise.all([...cardPromises, analysisPromise.then((r) => (analysisResult.value = r))])
  } catch (err) {
    console.error('Dashboard load error:', err)
    loadError.value = true
  } finally {
    loading.value = false
  }
}

type Breakdown = Array<{ value: string; count: number; percentage: number }>

/** One rendered tile: the card plus everything the template needs derived. */
interface CardView {
  card: DashboardCard
  key: string
  count: number
  breakdown: Breakdown
  rows: Entity[]
}

function buildBreakdown(card: DashboardCard, entities: Entity[]): Breakdown {
  if (!card.group_by) return []

  const groupBy = card.group_by
  const counts: Record<string, number> = {}
  let total = 0

  for (const entity of entities) {
    const value = String(entity.properties[groupBy] || 'Unknown')
    counts[value] = (counts[value] || 0) + 1
    total++
  }

  return Object.entries(counts)
    .map(([value, count]) => ({
      value,
      count,
      percentage: total > 0 ? (count / total) * 100 : 0,
    }))
    .sort((a, b) => b.count - a.count)
}

function buildRows(card: DashboardCard, entities: Entity[]): Entity[] {
  const sorted = [...entities]

  if (card.sort?.length) {
    const sort = card.sort[0]
    sorted.sort((a, b) => {
      const aVal = String(a.properties[sort.property] || '')
      const bVal = String(b.properties[sort.property] || '')
      const cmp = aVal.localeCompare(bVal)
      return sort.direction === 'desc' ? -cmp : cmp
    })
  }

  return card.limit ? sorted.slice(0, card.limit) : sorted
}

/**
 * The rendered tiles, with each card's display data derived exactly once
 * (TKT-ERHWL0).
 *
 * The template used to call `getBreakdown(card)` / `getTableRows(card)` twice
 * per card — once for `v-if="…length"`, once for `v-for` — and both are O(N)
 * or worse over the card's whole result set (a group-by; a copy plus a
 * `localeCompare` sort). Deriving into the view model the template iterates
 * means each derivation has exactly one call site, structurally, rather than
 * being deduplicated by a cache.
 *
 * One entry per card BY POSITION, deliberately not a Map keyed by `cardKey()`.
 * `cardKey` covers `[title, query, display]`, which is right for `cardData` —
 * two cards with the same query legitimately share one fetch — but wrong here,
 * because `group_by` / `sort` / `limit` change the derivation without changing
 * the key. Keying the derived data by `cardKey` let a "by status" card render a
 * "by priority" card's breakdown: the exact one-card's-data-on-another's-tile
 * bug `cardKey` was introduced (TKT-53KICM) to prevent.
 *
 * Deriving only for the display mode that renders it also keeps a count card
 * from paying for a table copy of a result set nothing shows.
 */
const cardViews = computed<CardView[]>(() =>
  cards.value.map((card) => {
    const key = cardKey(card)
    const entities = cardData.value.get(key)?.entities ?? []
    return {
      card,
      key,
      count: cardData.value.get(key)?.count || 0,
      breakdown: card.display === 'breakdown' ? buildBreakdown(card, entities) : [],
      rows: card.display === 'table' ? buildRows(card, entities) : [],
    }
  })
)

function getColumnLabel(col: { property?: string; label?: string }): string {
  return col.label || col.property || ''
}

function getCellValue(entity: Entity, col: { property?: string }): string {
  if (!col.property) return ''
  return String(entity.properties[col.property] || '')
}

function getCellLink(entity: Entity, col: { link?: string }): string | undefined {
  if (col.link === 'detail') {
    return `/entity/${entity.type}/${entity.id}`
  }
  return undefined
}

// Lifecycle
onMounted(async () => {
  // Belt-and-braces, NOT the thing preventing the empty-state flash: App.vue
  // renders <RouterView/> only after the store has loaded, so this view cannot
  // normally mount with `loaded === false`. It matters only if the boot
  // sequence changes, or when this component is mounted directly (as its unit
  // tests do). Without a gate somewhere, "No dashboard cards to show" — which
  // is indistinguishable from the all-filtered state — would flash on load.
  if (!schemaStore.loaded) {
    await schemaStore.load()
  }
  await loadData()
})
</script>

<template>
  <div class="dashboard-view" :data-testid="`page-state-${pageState}`">
    <header class="dashboard-header mobile-topbar mobile-topbar--with-menu">
      <h1>{{ title }}</h1>
      <p v-if="description" class="description">{{ description }}</p>
    </header>

    <div v-if="loading" class="loading-state">
      <div class="spinner"/>
      <span>Loading dashboard...</span>
    </div>

    <template v-else>
      <!-- Above the cards, and independent of them: a suggestion is worth
           showing even on a dashboard with nothing else on it. -->
      <NextActionCard />

      <!--
        No cards to show. Deliberately one state for three causes: no
        `dashboard:` configured, an empty `cards:`, and every card filtered out
        by `permission:` (TKT-53KICM). Distinguishing them would tell a user
        about cards they cannot use, which is the opposite of the point.
      -->
      <p v-if="cards.length === 0" class="no-data dashboard-empty">
        No dashboard cards to show.
      </p>

      <div v-else class="dashboard-grid">
        <section
          v-for="view in cardViews"
          :key="view.key"
          class="dashboard-card"
          :aria-labelledby="`dashboard-card-${view.key}-title`"
        >
          <div class="card-header">
            <h3 :id="`dashboard-card-${view.key}-title`">{{ view.card.title }}</h3>
            <router-link
              :to="`/search?q=${encodeURIComponent(view.card.query)}`"
              class="card-link"
              title="View in search"
            >
              &#8599;
            </router-link>
          </div>

          <!-- Count display -->
          <div v-if="view.card.display === 'count'" class="card-count">
            <span class="count-number">{{ view.count }}</span>
          </div>

          <!-- Breakdown display -->
          <div v-else-if="view.card.display === 'breakdown'" class="card-breakdown">
            <div
              v-for="item in view.breakdown"
              :key="item.value"
              class="breakdown-row"
            >
              <span class="breakdown-label">{{ item.value }}</span>
              <div class="breakdown-bar-track">
                <div
                  class="breakdown-bar-fill"
                  :style="{ width: `${item.percentage}%` }"
                />
              </div>
              <span class="breakdown-count">{{ item.count }}</span>
            </div>
            <div v-if="view.breakdown.length === 0" class="no-data">
              No data
            </div>
          </div>

          <!-- Table display -->
          <div v-else-if="view.card.display === 'table'" class="card-table">
            <table v-if="view.rows.length > 0" :aria-labelledby="`dashboard-card-${view.key}-title`">
              <thead>
                <tr>
                  <th v-for="col in view.card.columns" :key="col.property" scope="col">
                    {{ getColumnLabel(col) }}
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="entity in view.rows" :key="entity.id">
                  <td v-for="col in view.card.columns" :key="col.property">
                    <router-link
                      v-if="getCellLink(entity, col)"
                      :to="getCellLink(entity, col)!"
                      class="cell-link"
                    >
                      {{ getCellValue(entity, col) }}
                    </router-link>
                    <span v-else>{{ getCellValue(entity, col) }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
            <div v-else class="no-data">No results</div>
          </div>
        </section>
      </div>

      <!-- Validation card -->
      <div class="validation-card">
        <div class="card-header">
          <h3>&#9888; Validation</h3>
          <router-link to="/analyze" class="card-link" title="View full analysis">
            &#8599;
          </router-link>
        </div>
        <div class="validation-content">
          <template v-if="analysisResult">
            <span
              v-if="analysisResult.errors === 0 && analysisResult.warnings === 0"
              class="validation-success"
            >
              &#10003; All checks passed
            </span>
            <template v-else>
              <span v-if="analysisResult.errors > 0" class="badge badge-error">
                {{ analysisResult.errors }} {{ analysisResult.errors === 1 ? 'error' : 'errors' }}
              </span>
              <span v-if="analysisResult.warnings > 0" class="badge badge-warning">
                {{ analysisResult.warnings }} {{ analysisResult.warnings === 1 ? 'warning' : 'warnings' }}
              </span>
              <router-link to="/analyze" class="view-details">
                View details &rarr;
              </router-link>
            </template>
          </template>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.dashboard-view {
  max-width: 1200px;
}

.dashboard-header {
  margin-bottom: 24px;
}

.dashboard-header h1 {
  margin: 0 0 8px;
}

.description {
  color: var(--muted-text);
  margin: 0;
}

.loading-state {
  display: flex;
  align-items: center;
  gap: var(--space-md);
  padding: 48px;
  color: var(--muted-text);
}

.spinner {
  width: 24px;
  height: 24px;
  border: 3px solid var(--border-color);
  border-top-color: var(--accent-color);
  border-radius: var(--radius-circle);
  animation: spin 1s linear infinite;
}

.dashboard-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 20px;
  margin-bottom: 20px;
}

.dashboard-card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  padding: 16px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.card-header h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-color);
}

.card-link {
  color: var(--muted-text);
  text-decoration: none;
  font-size: var(--font-size-base);
}

.card-link:hover {
  color: var(--accent-color);
}

/* Count display */
.card-count {
  padding: 16px 0;
}

.count-number {
  font-size: 48px;
  font-weight: 700;
  color: var(--text-color);
}

/* Breakdown display */
.card-breakdown {
  display: flex;
  flex-direction: column;
  gap: var(--space-md);
}

.breakdown-row {
  display: flex;
  align-items: center;
  gap: var(--space-md);
}

.breakdown-label {
  min-width: 80px;
  font-size: var(--font-size-dense);
  color: var(--muted-text);
}

.breakdown-bar-track {
  flex: 1;
  height: 8px;
  background: var(--hover-bg);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.breakdown-bar-fill {
  height: 100%;
  background: var(--accent-color, #6366f1);
  border-radius: var(--radius-sm);
  transition: width 0.3s ease;
}

.breakdown-count {
  min-width: 32px;
  text-align: right;
  font-size: var(--font-size-dense);
  font-weight: 600;
  color: var(--text-color);
}

/* Table display */
.card-table {
  overflow-x: auto;
}

.card-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--font-size-dense);
}

.card-table th {
  text-align: left;
  padding: 8px;
  border-bottom: 1px solid var(--border-color);
  font-weight: 600;
  color: var(--muted-text);
}

.card-table td {
  padding: 8px;
  border-bottom: 1px solid var(--border-color);
  color: var(--text-color);
}

.cell-link {
  color: var(--accent-color);
  text-decoration: none;
}

.cell-link:hover {
  text-decoration: underline;
}

.no-data {
  color: var(--muted-text);
  font-size: var(--font-size-dense);
  padding: 8px 0;
}

/* Validation card */
.validation-card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  padding: 16px;
}

.validation-content {
  display: flex;
  align-items: center;
  gap: var(--space-md);
}

.validation-success {
  color: var(--success-color);
  font-weight: 600;
  font-size: var(--font-size-base);
}

.badge {
  font-size: var(--font-size-sm);
  padding: 4px 8px;
  border-radius: var(--radius-sm);
  font-weight: 500;
}

.badge-error {
  background: color-mix(in srgb, var(--error-color) 15%, transparent);
  color: var(--error-color);
}

.badge-warning {
  background: color-mix(in srgb, var(--warning-color) 15%, transparent);
  color: var(--warning-color);
}

.view-details {
  margin-left: auto;
  font-size: var(--font-size-dense);
  color: var(--accent-color);
  text-decoration: none;
  font-weight: 500;
}

.view-details:hover {
  text-decoration: underline;
}

@media (max-width: 768px) {
  /* .dashboard-header uses .mobile-topbar.mobile-topbar--with-menu from
     mobile-bars.css. Override only typography and hide the description
     to keep the bar compact. */
  .dashboard-header h1 {
    font-size: var(--font-size-lg);
    margin: 0;
  }

  .dashboard-header .description {
    display: none;
  }

  .dashboard-grid {
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: var(--space-md);
  }

  .dashboard-card {
    padding: 12px;
  }

  .card-header {
    margin-bottom: 8px;
  }

  /* Count cards: compact on mobile so the big number doesn't waste a
     screenful of vertical space. The number sits inline with the header
     instead of below it. */
  .card-count {
    padding: 0;
  }

  .count-number {
    font-size: var(--font-size-3xl);
  }

  .breakdown-label {
    min-width: 60px;
    font-size: var(--font-size-sm);
  }

  .breakdown-row {
    gap: var(--space-sm);
  }
}

@media (max-width: 480px) {
  .dashboard-grid {
    /* Two compact stat cards per row instead of one tall one. minmax(0, …)
       lets a track shrink below item min-content — with plain 1fr the
       full-width table cards blow the tracks past the viewport. */
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  /* Breakdown and table cards still want full width — too cramped at
     half width. */
  .dashboard-card:has(.card-breakdown),
  .dashboard-card:has(.card-table) {
    grid-column: 1 / -1;
  }
}
</style>
