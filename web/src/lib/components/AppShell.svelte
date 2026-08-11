<script lang="ts">
  /**
   * Persistent app chrome: desktop sidebar nav (shadcn-svelte Sidebar,
   * icon-collapsible), topbar with global type-ahead search and the user
   * menu, and a mobile bottom nav whose "More" button opens the SAME
   * sidebar as a full-screen sheet (Sidebar's built-in mobile behavior).
   * Mounted once by the root layout; page content renders into the default
   * slot.
   */
  import { onDestroy, onMount } from 'svelte';
  import { afterNavigate, goto } from '$app/navigation';
  import { page } from '$app/state';
  import Bell from '@lucide/svelte/icons/bell';
  import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
  import FileText from '@lucide/svelte/icons/file-text';
  import LogOut from '@lucide/svelte/icons/log-out';
  import Search from '@lucide/svelte/icons/search';
  import UserRound from '@lucide/svelte/icons/user-round';
  import Users from '@lucide/svelte/icons/users';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { navItems, mobilePrimaryItems } from '$lib/nav';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';
  import { clearToastHistory, toastHistory } from '$lib/toast';
  import type { DiscoverMediaItem, DiscoverSearchResult, User } from '$lib/types';
  import * as Sidebar from '$lib/components/ui/sidebar/index.js';
  import * as Popover from '$lib/components/ui/popover/index.js';
  import * as DropdownMenu from '$lib/components/ui/dropdown-menu/index.js';
  import * as Avatar from '$lib/components/ui/avatar/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import MobileMoreButton from '$lib/components/ui/sidebar/mobile-more-button.svelte';

  let globalSearch = '';
  let suggestions: DiscoverSearchResult | null = null;
  let searchOpen = false;
  let searchBusy = false;
  let searchToken = 0;
  let debounceTimer: number | undefined;
  let currentUser: User | null = null;
  let appVersion = '';
  let notifOpen = false;

  /** Formats a toast's timestamp as a short relative time (e.g. "3m ago"). */
  function relativeTime(at: number): string {
    const seconds = Math.max(0, Math.round((Date.now() - at) / 1000));
    if (seconds < 5) return 'just now';
    if (seconds < 60) return `${seconds}s ago`;
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.round(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.round(hours / 24)}d ago`;
  }

  // Reading page.url.pathname from a $: block does not reliably re-run on
  // client-side navigation in this app -- confirmed the same root cause as
  // +layout.svelte's isPublic bug. afterNavigate + a plain `let`, plus an
  // eager synchronous read for the very first load (afterNavigate does not
  // fire for that one), is the reliable alternative.
  let currentPath: string = page.url.pathname;
  let activeHrefs = new Set<string>();
  function recomputeActiveHrefs() {
    activeHrefs = new Set(
      navItems
        .filter((item) => (item.href === '/dashboard' && currentPath === '/') || currentPath === item.href || currentPath.startsWith(`${item.href}/`))
        .map((item) => item.href)
    );
  }
  recomputeActiveHrefs();
  afterNavigate((nav) => {
    currentPath = nav.to?.url.pathname ?? window.location.pathname;
    recomputeActiveHrefs();
  });

  function submitSearch() {
    const q = globalSearch.trim();
    if (!q) return;
    searchOpen = false;
    void goto(`/search?q=${encodeURIComponent(q)}`);
  }

  function suggestionKey(item: DiscoverMediaItem) {
    return `${item.mediaType}:${item.tmdbId ?? item.imdbId ?? item.title}`;
  }

  function openSuggestion(item: DiscoverMediaItem) {
    searchOpen = false;
    void goto(detailsHref(item));
  }

  async function runSuggest(query: string) {
    const token = ++searchToken;
    if (query.trim().length < 2) { suggestions = null; searchBusy = false; return; }
    searchBusy = true;
    try {
      const result = await api.discoverSearch(query.trim());
      if (token !== searchToken) return;
      suggestions = result;
      searchOpen = true;
    } catch {
      if (token !== searchToken) return;
      suggestions = null;
    } finally {
      if (token === searchToken) searchBusy = false;
    }
  }

  function scheduleSuggest() {
    window.clearTimeout(debounceTimer);
    debounceTimer = window.setTimeout(() => void runSuggest(globalSearch), 220);
  }

  function onInput() {
    if (!globalSearch.trim()) { searchOpen = false; suggestions = null; searchBusy = false; window.clearTimeout(debounceTimer); return; }
    scheduleSuggest();
  }

  function onBlur() { window.setTimeout(() => { searchOpen = false; }, 120); }

  async function loadMe() {
    try {
      currentUser = await api.me();
    } catch {
      currentUser = null;
    }
  }

  async function logout() {
    await api.logout();
    currentUser = null;
    void goto('/login', { replaceState: true });
  }

  $: if (currentPath !== '/search' && !globalSearch) suggestions = null;

  onMount(() => {
    void loadMe();
    void api.status().then((s) => (appVersion = s.version)).catch(() => {});
  });
  onDestroy(() => window.clearTimeout(debounceTimer));
</script>

<Sidebar.Provider>
  <Sidebar.Root collapsible="icon">
    <Sidebar.Header>
      <a href="/dashboard" class="flex items-center gap-2" aria-label="Dashboard">
        <span class="grid size-8 shrink-0 place-items-center rounded-lg bg-primary text-primary-foreground">
          <DrakkarLogo size={18} />
        </span>
        <span class="text-sm font-semibold group-data-[collapsible=icon]:hidden">Drakkar</span>
      </a>
    </Sidebar.Header>
    <Sidebar.Content>
      <Sidebar.Group>
        <Sidebar.GroupContent>
          <Sidebar.Menu class="gap-1">
            {#each navItems as item}
              <Sidebar.MenuItem>
                <Sidebar.MenuButton isActive={activeHrefs.has(item.href)} tooltipContent={item.label}>
                  {#snippet child({ props })}
                    <a href={item.href} aria-label={item.label} {...props} data-active={activeHrefs.has(item.href) ? 'true' : undefined}>
                      <svelte:component this={item.icon} />
                      <span class="truncate group-data-[collapsible=icon]:hidden">{item.label}</span>
                    </a>
                  {/snippet}
                </Sidebar.MenuButton>
              </Sidebar.MenuItem>
            {/each}
          </Sidebar.Menu>
        </Sidebar.GroupContent>
      </Sidebar.Group>
    </Sidebar.Content>
    <Sidebar.Footer>
      {#if appVersion}
        <div class="px-2 pb-1 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">Drakkar v{appVersion}</div>
      {/if}
      <Sidebar.Menu>
        <Sidebar.MenuItem>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger>
              {#snippet child({ props })}
                <Sidebar.MenuButton size="lg" class="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground" {...props}>
                  <Avatar.Root class="size-8 rounded-lg">
                    <Avatar.Fallback class="rounded-lg bg-primary text-primary-foreground">
                      <UserRound size={16} />
                    </Avatar.Fallback>
                  </Avatar.Root>
                  <div class="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                    <span class="truncate font-medium">{currentUser?.username ?? 'Account'}</span>
                    <span class="truncate text-xs text-muted-foreground">{currentUser?.role ?? 'guest'}</span>
                  </div>
                  <ChevronsUpDown class="ms-auto size-4 group-data-[collapsible=icon]:hidden" />
                </Sidebar.MenuButton>
              {/snippet}
            </DropdownMenu.Trigger>
            <DropdownMenu.Content align="end" side="top" class="w-56">
              {#if currentUser}
                <div class="px-2 py-1.5 text-sm">
                  <div class="font-medium">{currentUser.username}</div>
                  <div class="text-xs text-muted-foreground">{currentUser.role}</div>
                </div>
                <DropdownMenu.Separator />
              {/if}
              <DropdownMenu.Item onclick={() => goto('/users')}>
                <Users class="size-4" />
                Users
              </DropdownMenu.Item>
              <DropdownMenu.Item onclick={logout}>
                <LogOut class="size-4" />
                Log out
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Root>
        </Sidebar.MenuItem>
      </Sidebar.Menu>
    </Sidebar.Footer>
  </Sidebar.Root>

  <Sidebar.Inset>
    <header class="sticky top-0 z-20 grid grid-cols-[auto_1fr_auto] items-center gap-3 border-b bg-background/90 px-4 py-2.5 backdrop-blur-md">
      <Sidebar.Trigger />

      <div class="relative mx-auto w-full max-w-[520px]">
        <form on:submit|preventDefault={submitSearch} class="relative flex items-center">
          <Search class="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
          <input
            bind:value={globalSearch}
            type="search"
            placeholder="Search movies, shows..."
            aria-label="Global search"
            on:input={onInput}
            on:focus={() => { if (suggestions) searchOpen = true; }}
            on:blur={onBlur}
            class="h-9 w-full rounded-full border bg-muted/40 pl-9 pr-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          />
        </form>
        {#if searchOpen && (searchBusy || suggestions)}
          <div class="absolute left-0 top-[calc(100%+8px)] z-30 w-full overflow-hidden rounded-lg bg-popover text-popover-foreground shadow-md ring-1 ring-foreground/10">
            {#if searchBusy && !suggestions}
              <div class="p-4 text-sm text-muted-foreground">Searching…</div>
            {:else if suggestions && !(suggestions.movies.length || suggestions.tv.length)}
              <div class="p-4 text-sm text-muted-foreground">No results.</div>
            {:else if suggestions}
              {#if suggestions.movies.length}
                <div class="p-1">
                  <div class="px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">Movies</div>
                  {#each suggestions.movies.slice(0, 5) as item (suggestionKey(item))}
                    <button type="button" class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm outline-none hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50" on:mousedown|preventDefault={() => openSuggestion(item)}>
                      <span class="truncate">{item.title}</span>
                      <span class="text-xs text-muted-foreground">{item.year || '—'}</span>
                    </button>
                  {/each}
                </div>
              {/if}
              {#if suggestions.tv.length}
                <div class="p-1">
                  <div class="px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">TV Shows</div>
                  {#each suggestions.tv.slice(0, 5) as item (suggestionKey(item))}
                    <button type="button" class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm outline-none hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50" on:mousedown|preventDefault={() => openSuggestion(item)}>
                      <span class="truncate">{item.title}</span>
                      <span class="text-xs text-muted-foreground">{item.year || '—'}</span>
                    </button>
                  {/each}
                </div>
              {/if}
              <button type="submit" on:click={submitSearch} class="block w-full border-t px-3 py-2 text-center text-sm font-medium text-primary outline-none hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50">Open full search</button>
            {/if}
          </div>
        {/if}
      </div>

      <div class="flex items-center gap-1.5">
        <Button variant="ghost" size="icon" href="/docs" target="_blank" rel="noreferrer" title="API docs" aria-label="API docs">
          <FileText class="size-4" />
        </Button>

        <Popover.Root bind:open={notifOpen}>
          <Popover.Trigger>
            {#snippet child({ props })}
              <Button variant="ghost" size="icon" title="Notifications" aria-label="Notifications" {...props}>
                <Bell class="size-4" />
              </Button>
            {/snippet}
          </Popover.Trigger>
          <Popover.Content align="end" class="w-80 p-0">
            <div class="flex items-center justify-between border-b px-3.5 py-3 text-sm font-semibold">
              <span>Notifications</span>
              {#if $toastHistory.length}
                <button type="button" class="rounded-sm text-xs font-normal text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50" on:click={clearToastHistory}>Clear</button>
              {/if}
            </div>
            <div class="max-h-[360px] overflow-y-auto">
              {#if $toastHistory.length === 0}
                <div class="p-6 text-center text-sm text-muted-foreground">No notifications yet.</div>
              {:else}
                {#each $toastHistory as item (item.id)}
                  <div
                    class="border-b border-l-[3px] px-3.5 py-2.5"
                    class:border-l-primary={item.tone === 'info'}
                    style={item.tone === 'success' ? 'border-left-color: hsl(var(--status-available))' : item.tone === 'error' ? 'border-left-color: hsl(var(--status-failed))' : undefined}
                  >
                    <div class="text-sm leading-snug">{item.message}</div>
                    <div class="mt-0.5 text-xs text-muted-foreground">{relativeTime(item.at)}</div>
                  </div>
                {/each}
              {/if}
            </div>
          </Popover.Content>
        </Popover.Root>
      </div>
    </header>

    <main class="mx-auto w-full max-w-[1760px] flex-1 px-4 py-2 pb-24 md:pb-8">
      <slot />
    </main>

    <!-- Mobile bottom nav: 5 primary + More (opens the full sidebar sheet) -->
    <nav class="fixed inset-x-0 bottom-0 z-20 flex h-16 items-center justify-around border-t bg-background/95 backdrop-blur-md md:hidden" aria-label="Primary navigation">
      {#each mobilePrimaryItems as item}
        <a href={item.href} class="relative flex min-h-11 min-w-11 flex-col items-center justify-center gap-0.5 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50" class:text-primary={activeHrefs.has(item.href)} class:text-muted-foreground={!activeHrefs.has(item.href)} aria-label={item.label} title={item.label}>
          <svelte:component this={item.icon} size={20} />
          {#if activeHrefs.has(item.href)}<span class="absolute -bottom-0.5 size-1 rounded-full bg-primary"></span>{/if}
        </a>
      {/each}
      <MobileMoreButton />
    </nav>
  </Sidebar.Inset>
</Sidebar.Provider>
