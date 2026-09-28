<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useSchemaStore, useUIStore, useGitStore } from '@/stores'
import { getSidebar, runAction } from '@/api'
import { isCancelledFetch } from '@/composables/usePageData'
import { useActionFeedback } from '@/composables/useActionFeedback'
import type { SidebarGroup, SidebarItem } from '@/types'
import { isInputFocused } from '@/utils/dom'
import {
  IconApps,
  IconMoon,
  IconSearch,
  IconSettings,
  IconSun,
  IconWarning,
} from '@/utils/icons'
import NavIcon from './NavIcon.vue'
import SidebarEntityQuery from './SidebarEntityQuery.vue'
import { useEvents } from '@/composables/useEvents'
import { useSidebarEmptyGroups } from '@/composables/useSidebarEmptyGroups'
import ProjectSwitcher from './ProjectSwitcher.vue'
import { apiUrl } from '@/api/base'

const schemaStore = useSchemaStore()
const uiStore = useUIStore()
const gitStore = useGitStore()
const { reportResult, reportError } = useActionFeedback()
const route = useRoute()
const router = useRouter()

// Tracks which action items are currently in-flight (prevents double-click).
const actionInFlight = ref<Set<string>>(new Set())

// Sidebar data from API
const sidebarGroups = ref<SidebarGroup[]>([])
const sidebarAppName = ref('')

const appName = computed(() => sidebarAppName.value || schemaStore.app.name)

// Custom apps surfaced as sidebar links. The label falls back to title, then
// the id, so an app with no metadata still gets a usable entry.
const appLinks = computed(() =>
  Array.from(schemaStore.apps.entries()).map(([id, app]) => ({
    id,
    label: app.label || app.title || id,
  })),
)
// Logo lives on the schema store so SettingsView can update it after
// upload/remove without a sidebar refetch.
const logoUrl = computed(() => schemaStore.logoUrl)

const {
  entryKey,
  itemKey,
  setEntryShown,
  groupIsEmpty,
  reset: resetEmptyGroups,
} = useSidebarEmptyGroups()

// Load generation for itemKey; sidebarRequest drops a response overtaken by a
// later load, so two quick config reloads keep the newer navigation.
const sidebarLoad = ref(0)
let sidebarRequest = 0

// Load sidebar data
async function loadSidebar() {
  const request = ++sidebarRequest
  try {
    const data = await getSidebar()
    if (request !== sidebarRequest) return
    sidebarAppName.value = data.app.name
    resetEmptyGroups()
    sidebarLoad.value++
    sidebarGroups.value = data.navigation
    schemaStore.setLogoUrl(data.logoUrl ?? null)
    // Principal-scoped inline-create offers ride on this payload; see
    // SidebarData.inline_create for why the sidebar carries them.
    schemaStore.setInlineCreate(data.inline_create ?? {})
  } catch (err) {
    // Suppress cancellation errors from rapid navigation in Firefox
    // (see BUG-6C3V and src/composables/usePageData.ts).
    if (isCancelledFetch(err)) return
    console.error('Failed to load sidebar:', err)
  }
}

// Keyboard shortcut for search.
//
// Defers to a list view's in-place search box when one is rendered — list
// views own their own search affordance now (TKT-603FQ), and jumping to the
// standalone /search page would surprise users mid-list. The fallback
// behavior (push /search) still applies on routes without a search box.
function handleKeydown(e: KeyboardEvent) {
  if (e.key !== '/') return
  if (isInputFocused()) return
  if (document.querySelector('.entity-list .search-box')) return
  e.preventDefault()
  router.push('/search')
}

// Close mobile sidebar on route change
watch(() => route.path, () => {
  if (uiStore.sidebarMobileOpen) {
    uiStore.closeMobileSidebar()
  }
})

// Lock body scroll when mobile sidebar is open
watch(() => uiStore.sidebarMobileOpen, (open) => {
  document.body.style.overflow = open ? 'hidden' : ''
})

function handleKeydownAll(e: KeyboardEvent) {
  handleKeydown(e)
  if (e.key === 'Escape' && uiStore.sidebarMobileOpen) {
    uiStore.closeMobileSidebar()
  }
}

// A `refresh` is sent when data-entry.yaml reloads. Refetching keeps an
// `entities:` entry from requesting a scope the new config removed, which
// would otherwise show "Could not load" until a full page reload.
const { on: onEvent } = useEvents()
onEvent('refresh', loadSidebar)

onMounted(() => {
  document.addEventListener('keydown', handleKeydownAll)
  loadSidebar()
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydownAll)
  document.body.style.overflow = ''
})

function isActive(href: string): boolean {
  return route.path === href || route.path.startsWith(href + '/')
}

// Icon names come from config and resolve through the shared allowlist
// registry (utils/icons.ts); unknown names fall back to a default rather than
// throwing, so a stale config still renders. This used to be a local switch
// returning emoji, which could not take `currentColor`, ignored the theme, and
// rendered differently on every OS.

async function handleAction(item: SidebarItem, ev?: Event) {
  if (!item.action) return
  if (actionInFlight.value.has(item.action)) return

  const triggerEl =
    ev && ev.currentTarget instanceof HTMLElement ? ev.currentTarget : null

  actionInFlight.value.add(item.action)
  try {
    const response = await runAction(item.action)
    reportResult(response)
    if (response?.redirect) {
      router.push(response.redirect)
    }
  } catch (err: unknown) {
    reportError(err, triggerEl)
  } finally {
    actionInFlight.value.delete(item.action)
  }
}
</script>

<template>
  <aside
    id="main-sidebar"
    class="sidebar"
    :class="{ collapsed: uiStore.sidebarCollapsed, 'mobile-open': uiStore.sidebarMobileOpen }"
  >
    <div class="sidebar-header">
      <RouterLink to="/" class="logo" :aria-label="appName">
        <img v-if="logoUrl" :src="apiUrl(logoUrl)" :alt="appName" class="logo-img" />
        <span v-else>{{ appName }}</span>
      </RouterLink>
      <button class="collapse-btn" @click="uiStore.toggleSidebar">
        {{ uiStore.sidebarCollapsed ? '→' : '←' }}
      </button>
    </div>

    <!-- Only renders when the host has more than one project open. -->
    <div v-if="!uiStore.sidebarCollapsed" class="sidebar-switcher">
      <ProjectSwitcher />
    </div>

    <!-- Fixed top items: Search and Analysis -->
    <div class="sidebar-top-items">
      <RouterLink to="/search" class="nav-item" :class="{ active: route.path === '/search' }">
        <component :is="IconSearch" class="nav-icon" :size="18" aria-hidden="true" />
        <span class="nav-label">Search</span>
        <kbd v-if="!uiStore.sidebarCollapsed">/</kbd>
      </RouterLink>
      <RouterLink to="/analyze" class="nav-item" :class="{ active: route.path === '/analyze' }">
        <component :is="IconWarning" class="nav-icon" :size="18" aria-hidden="true" />
        <span class="nav-label">Analysis</span>
      </RouterLink>
    </div>

    <nav class="sidebar-nav">
      <!-- An ungrouped entry is a group without a title. -->
      <div
        v-for="(group, index) in sidebarGroups"
        v-show="!groupIsEmpty(group, index)"
        :key="index"
        :class="{ 'nav-section': group.group }"
      >
        <div v-if="group.group" class="nav-section-title">{{ group.group }}</div>
        <template v-for="(item, itemIndex) in group.items" :key="itemKey(item, index, itemIndex, sidebarLoad)">
          <button
            v-if="item.action"
            type="button"
            class="nav-item nav-action"
            :aria-label="item.label"
            :disabled="actionInFlight.has(item.action)"
            @click="handleAction(item, $event)"
          >
            <NavIcon
              :name="item.icon"
              :fallback="item.derivedIcon"
              :collapsed="uiStore.sidebarCollapsed"
            />
            <span class="nav-label">{{ item.label }}</span>
          </button>
          <RouterLink
            v-else-if="item.href"
            :to="item.href"
            class="nav-item"
            :class="{ active: isActive(item.href) }"
          >
            <NavIcon
              :name="item.icon"
              :fallback="item.derivedIcon"
              :collapsed="uiStore.sidebarCollapsed"
            />
            <span class="nav-label">{{ item.label }}</span>
          </RouterLink>
          <SidebarEntityQuery
            v-else-if="item.entities"
            v-slot="{ rows, overflow, failed }"
            :entities="item.entities"
            @shown="(shown: boolean) => setEntryShown(entryKey(index, itemIndex), shown)"
          >
            <RouterLink
              v-for="row in rows"
              :key="row.key"
              :to="row.to"
              class="nav-item nav-entity"
              :class="{ active: isActive(row.path) }"
              :title="row.title"
            >
              <NavIcon :name="item.icon" :fallback="item.derivedIcon" :collapsed="uiStore.sidebarCollapsed" />
              <span class="nav-label">{{ row.title }}</span>
            </RouterLink>
            <div v-if="overflow > 0" class="nav-note nav-label">and {{ overflow }} more</div>
            <div v-if="failed" class="nav-note nav-label" role="status">Could not load</div>
          </SidebarEntityQuery>
        </template>
      </div>

      <!-- Custom apps (sandboxed-iframe extensions). Routes to /app/:id. -->
      <div v-if="appLinks.length" class="nav-section">
        <div class="nav-section-title">Apps</div>
        <RouterLink
          v-for="app in appLinks"
          :key="app.id"
          :to="`/app/${app.id}`"
          class="nav-item"
          :class="{ active: isActive(`/app/${app.id}`) }"
        >
          <component :is="IconApps" class="nav-icon" :size="18" aria-hidden="true" />
          <span class="nav-label">{{ app.label }}</span>
        </RouterLink>
      </div>
    </nav>

    <!-- Mobile-only footer: git status, settings, theme toggle -->
    <div class="sidebar-mobile-footer">
      <div v-if="gitStore.isAvailable" class="mobile-git-status" :class="gitStore.statusClass">
        <span class="mobile-git-dot"/>
        <span class="nav-label">{{ gitStore.branch }} · {{ gitStore.statusText }}</span>
      </div>
      <RouterLink to="/settings" class="nav-item" :class="{ active: route.path === '/settings' }">
        <component :is="IconSettings" class="nav-icon" :size="18" aria-hidden="true" />
        <span class="nav-label">Settings</span>
      </RouterLink>
      <button
        v-if="!schemaStore.darkDisabled"
        class="nav-item nav-action"
        @click="uiStore.toggleDarkMode()"
      >
        <component :is="uiStore.isDark ? IconSun : IconMoon" class="nav-icon" :size="18" aria-hidden="true" />
        <span class="nav-label">{{ uiStore.isDark ? 'Light Mode' : 'Dark Mode' }}</span>
      </button>
    </div>

    <Teleport to="body">
      <div
        v-if="uiStore.sidebarMobileOpen"
        class="sidebar-backdrop"
        @click="uiStore.closeMobileSidebar()"
      />
    </Teleport>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 240px;
  height: calc(100vh - 24px); /* Account for status bar */
  background: var(--sidebar-bg, #1a1a2e);
  color: var(--sidebar-text, #e8e8e8);
  display: flex;
  flex-direction: column;
  position: fixed;
  left: 0;
  top: 0;
  transition: width 0.2s ease;
  z-index: 100;
}

.sidebar.collapsed {
  width: 60px;
}

.sidebar.collapsed .nav-label,
.sidebar.collapsed .nav-section-title,
.sidebar.collapsed .logo {
  display: none;
}

/* Collapsed, entity links would be a column of identical icons. */
.sidebar.collapsed .nav-entity {
  display: none;
}

.sidebar-header {
  padding: 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid rgba(255, 255, 255, 0.1);
}

.logo {
  font-weight: 600;
  font-size: 18px;
  color: inherit;
  text-decoration: none;
  display: flex;
  align-items: center;
  min-width: 0;
}

.logo-img {
  max-height: 28px;
  max-width: 100%;
  object-fit: contain;
  display: block;
}

.collapse-btn {
  background: none;
  border: none;
  color: inherit;
  cursor: pointer;
  padding: 4px 8px;
  opacity: 0.7;
}

.collapse-btn:hover {
  opacity: 1;
}

.sidebar-switcher {
  padding: 0 12px 8px;
}

.sidebar-top-items {
  padding: 8px 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.1);
}

.sidebar-nav {
  flex: 1;
  overflow-y: auto;
  padding: 8px 0;
}

.nav-section {
  margin-bottom: 8px;
}

.nav-section-title {
  padding: 8px 16px;
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  opacity: 0.6;
}

.nav-item {
  display: flex;
  align-items: center;
  padding: 10px 16px;
  color: inherit;
  text-decoration: none;
  transition: background 0.15s ease;
}

.nav-item:hover {
  background: rgba(255, 255, 255, 0.1);
}

.nav-item.active {
  background: rgba(255, 255, 255, 0.15);
  border-right: 3px solid var(--accent-color, #6366f1);
}

/* SVG icons, not emoji. The icon inherits `currentColor` from .nav-item, which
 * is the whole reason for the swap — an emoji could not take the theme's
 * colour, so it stayed the same hue in light and dark mode and on hover.
 *
 * Do NOT set `width` here to make the gutter. Lucide emits width/height as
 * PRESENTATION ATTRIBUTES, and CSS beats those — so `width: 24px` overrode the
 * 18px attribute while `height` stayed at 18, rendering every icon 24x18 (a
 * 4:3 horizontal stretch, easy to miss because a squashed circle still reads
 * as a circle). Size comes from the `:size` prop; the 24px gutter the labels
 * align to comes from the box model instead. */
.nav-icon {
  /* Size the BOX to the icon (18px, matching the :size prop) and make up the
   * 24px gutter with margin. flex-basis is not an option: on a row flex item
   * it resolves to the main-size, i.e. width — the same trap as setting
   * `width` directly. */
  flex: 0 0 auto;
  margin-right: 18px;
}

.sidebar.collapsed .nav-icon {
  margin-right: 0;
}

.nav-label {
  font-size: 14px;
  flex: 1;
}

/* Entity titles can be long: one line per link, full title in the tooltip. */
.nav-entity .nav-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Overflow count or load error under an `entities:` entry, label-indented. */
.nav-note {
  padding: 4px 16px 8px 58px;
  font-size: 12px;
  opacity: 0.6;
}

/* Action buttons in the sidebar — same look as RouterLink nav items */
.nav-action {
  width: 100%;
  background: none;
  border: none;
  color: inherit;
  text-align: left;
  cursor: pointer;
  font-family: inherit;
  font-size: inherit;
}

.nav-action:disabled {
  opacity: 0.5;
  cursor: wait;
}

/* Mobile footer — hidden on desktop */
.sidebar-mobile-footer {
  display: none;
}

.mobile-git-status {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  font-size: 13px;
  opacity: 0.7;
}

.mobile-git-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
  flex-shrink: 0;
}

.mobile-git-status.synced .mobile-git-dot {
  background: var(--success-color);
}

.mobile-git-status.changes .mobile-git-dot {
  background: var(--warning-color);
}

.mobile-git-status.conflict .mobile-git-dot {
  background: var(--error-color);
}

/* Mobile overlay */
@media (max-width: 768px) {
  .sidebar {
    transform: translateX(-100%);
    height: 100vh;
    padding-top: env(safe-area-inset-top, 0px);
    transition: transform 0.25s ease;
  }

  /* When the mobile sidebar is open, the hamburger button overlays the
     sidebar header. Indent the header so the title isn't covered. */
  .sidebar.mobile-open .sidebar-header {
    padding-left: 60px;
  }

  .sidebar.mobile-open {
    transform: translateX(0);
  }

  .sidebar.collapsed {
    width: 240px;
  }

  .sidebar.collapsed .nav-label,
  .sidebar.collapsed .nav-section-title,
  .sidebar.collapsed .logo {
    display: unset;
  }

  .sidebar.collapsed .nav-entity { display: flex; }

  .sidebar.collapsed .nav-icon {
    margin-right: 12px;
  }

  .collapse-btn {
    display: none;
  }

  .nav-item {
    padding: 12px 16px;
    min-height: 44px;
  }

  .sidebar-mobile-footer {
    display: block;
    border-top: 1px solid rgba(255, 255, 255, 0.1);
    padding: 8px 0;
    margin-top: auto;
  }
}
</style>

<style>
/* Backdrop must be unscoped to work with Teleport */
.sidebar-backdrop {
  display: none;
}

@media (max-width: 768px) {
  .sidebar-backdrop {
    display: block;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 99;
  }
}
</style>
