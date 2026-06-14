<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import Bell from '@lucide/svelte/icons/bell';
  import BookOpen from '@lucide/svelte/icons/book-open';
  import LogOut from '@lucide/svelte/icons/log-out';
  import Menu from '@lucide/svelte/icons/menu';
  import Search from '@lucide/svelte/icons/search';
  import X from '@lucide/svelte/icons/x';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { navItems, mobilePrimaryItems } from '$lib/nav';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';
  import type { DiscoverMediaItem, DiscoverSearchResult, User } from '$lib/types';

  let mobileOpen = false;
  let globalSearch = '';
  let suggestions: DiscoverSearchResult | null = null;
  let searchOpen = false;
  let searchBusy = false;
  let searchToken = 0;
  let debounceTimer: number | undefined;
  let currentUser: User | null = null;

  function isActive(href: string) {
    if (href === '/dashboard' && page.url.pathname === '/') return true;
    return page.url.pathname === href || page.url.pathname.startsWith(`${href}/`);
  }

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

  $: if (page.url.pathname !== '/search' && !globalSearch) suggestions = null;

  onMount(() => {
    void loadMe();
  });
  onDestroy(() => window.clearTimeout(debounceTimer));
</script>

<div class="shell">
  <!-- Desktop sidebar -->
  <aside class="sidebar">
    <a href="/dashboard" class="brandmark" aria-label="Dashboard">
      <DrakkarLogo size={22} />
    </a>
    <nav class="side-nav">
      {#each navItems as item}
        <a href={item.href} class:active={isActive(item.href)} title={item.label} aria-label={item.label}>
          <svelte:component this={item.icon} size={18} />
        </a>
      {/each}
    </nav>
    <div class="sidebar-tail">
      <a href="/settings?tab=logs" title="Logs" aria-label="Logs"><Bell size={18} /></a>
    </div>
  </aside>

  <!-- Content -->
  <div class="content-wrap">
    <header class="topbar">
      <button class="hamburger" type="button" aria-label="Open navigation" on:click={() => (mobileOpen = true)}>
        <Menu size={20} />
      </button>

      <form class="searchbar" on:submit|preventDefault={submitSearch}>
        <Search size={15} />
        <input bind:value={globalSearch} type="search" placeholder="Search movies, shows..."
          aria-label="Global search" on:input={onInput}
          on:focus={() => { if (suggestions) searchOpen = true; }} on:blur={onBlur} />
        {#if searchOpen && (searchBusy || suggestions)}
          <div class="search-popover">
            {#if searchBusy && !suggestions}
              <div class="search-state">Searching…</div>
            {:else if suggestions && !(suggestions.movies.length || suggestions.tv.length)}
              <div class="search-state">No results.</div>
            {:else if suggestions}
              {#if suggestions.movies.length}
                <div class="search-group">
                  <div class="search-label">Movies</div>
                  {#each suggestions.movies.slice(0, 5) as item (suggestionKey(item))}
                    <button class="search-item" type="button" on:mousedown|preventDefault={() => openSuggestion(item)}>
                      <span class="search-name">{item.title}</span>
                      <span class="search-meta">{item.year || '—'}</span>
                    </button>
                  {/each}
                </div>
              {/if}
              {#if suggestions.tv.length}
                <div class="search-group">
                  <div class="search-label">TV Shows</div>
                  {#each suggestions.tv.slice(0, 5) as item (suggestionKey(item))}
                    <button class="search-item" type="button" on:mousedown|preventDefault={() => openSuggestion(item)}>
                      <span class="search-name">{item.title}</span>
                      <span class="search-meta">{item.year || '—'}</span>
                    </button>
                  {/each}
                </div>
              {/if}
              <button class="search-all" type="submit">Open full search</button>
            {/if}
          </div>
        {/if}
      </form>

      <div class="topbar-right">
        <a class="icon-btn" href="/settings?tab=logs" aria-label="Logs"><BookOpen size={15} /></a>
        {#if currentUser}
          <a class="user-chip" href="/users" aria-label="Open users">
            <span class="user-name">{currentUser.username}</span>
            <span class="user-role">{currentUser.role}</span>
          </a>
        {/if}
        <button class="icon-btn" type="button" aria-label="Log out" on:click={logout}>
          <LogOut size={15} />
        </button>
        <div class="avatar"><DrakkarLogo size={18} /></div>
      </div>
    </header>

    <main class="main"><slot /></main>
  </div>

  <!-- Mobile overlay + drawer -->
  <div class:open={mobileOpen} class="mobile-overlay">
    <button class="mobile-backdrop" type="button" aria-label="Close" on:click={() => (mobileOpen = false)}></button>
    <div class="mobile-drawer" role="dialog" aria-modal="true" tabindex="-1">
      <div class="drawer-head">
        <div class="drawer-brand-wrap">
          <div class="drawer-brand-icon"><DrakkarLogo size={20} /></div>
          <div>
            <div class="drawer-brand">Drakkar</div>
            <div class="drawer-sub">control plane</div>
          </div>
        </div>
        <button class="icon-btn" type="button" aria-label="Close" on:click={() => (mobileOpen = false)}>
          <X size={18} />
        </button>
      </div>
      <nav class="drawer-nav">
        {#each navItems as item}
          <a href={item.href} class:active={isActive(item.href)} on:click={() => (mobileOpen = false)}>
            <svelte:component this={item.icon} size={18} />
            <span>{item.label}</span>
          </a>
        {/each}
      </nav>
    </div>
  </div>

  <!-- Mobile bottom nav: 5 primary + More -->
  <nav class="bottom-nav" aria-label="Primary navigation">
    {#each mobilePrimaryItems as item}
      <a href={item.href} class:active={isActive(item.href)} aria-label={item.label} title={item.label}>
        <svelte:component this={item.icon} size={20} />
        {#if isActive(item.href)}<span class="active-dot"></span>{/if}
      </a>
    {/each}
    <button class="bottom-more" type="button" on:click={() => (mobileOpen = true)} aria-label="More">
      <Menu size={20} />
    </button>
  </nav>
</div>

<style>
  .shell { min-height: 100vh; }

  /* ── Sidebar ─────────────────────────────────── */
  .sidebar {
    position: fixed; inset: 0 auto 0 0; z-index: 30;
    width: 56px; display: none; flex-direction: column;
    align-items: center; gap: 4px; padding: 14px 0;
    border-right: 1px solid hsl(171 80% 56% / 0.1);
    background: hsl(215 36% 4% / 0.78); backdrop-filter: blur(20px);
  }

  .brandmark {
    display: grid; place-items: center; width: 36px; height: 36px;
    border-radius: 12px; background: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
    text-decoration: none; margin-bottom: 8px;
  }

  .side-nav { display: flex; flex-direction: column; gap: 2px; flex: 1; padding-top: 4px; }
  .sidebar-tail { display: flex; flex-direction: column; gap: 6px; }

  .side-nav a, .sidebar-tail a {
    display: grid; place-items: center; width: 40px; height: 40px;
    border-radius: 14px; border: 1px solid transparent;
    color: hsl(var(--muted-foreground)); background: transparent; text-decoration: none;
    transition: background .12s, color .12s;
  }
  .side-nav a:hover, .sidebar-tail a:hover {
    color: hsl(var(--foreground)); background: hsl(0 0% 100% / 0.08);
  }
  .side-nav a.active {
    color: hsl(var(--foreground)); background: hsl(0 0% 100% / 0.12);
    border-color: hsl(0 0% 100% / 0.06);
  }

  /* ── Content wrap ────────────────────────────── */
  .content-wrap { min-width: 0; }

  .topbar {
    position: sticky; top: 0; z-index: 20; display: flex;
    align-items: center; gap: 12px; padding: 12px 16px;
    background: linear-gradient(180deg, hsl(var(--background) / 0.96), hsl(var(--background) / 0.4));
    backdrop-filter: blur(18px);
  }

  .hamburger {
    display: grid; place-items: center; width: 44px; height: 44px;
    border-radius: 16px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.05); color: hsl(var(--foreground));
    cursor: pointer; flex-shrink: 0;
  }

  /* ── Search ──────────────────────────────────── */
  .searchbar {
    position: relative; flex: 1; display: flex; align-items: center;
    gap: 10px; height: 44px; padding: 0 16px;
    border: 1px solid hsl(171 80% 56% / 0.12); border-radius: 18px;
    background: hsl(171 30% 40% / 0.05); color: hsl(var(--muted-foreground));
  }
  .searchbar input {
    flex: 1; border: none; outline: none; background: transparent;
    color: hsl(var(--foreground)); font-size: 14px; font-weight: 600; min-width: 0;
  }
  .searchbar input::placeholder { color: hsl(var(--muted-foreground)); }

  .search-popover {
    position: absolute; top: calc(100% + 10px); left: 0; right: 0; z-index: 100;
    display: grid; gap: 12px; padding: 14px;
    border-radius: 18px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(221 39% 7% / 0.98); box-shadow: 0 20px 50px hsl(0 0% 0% / 0.4);
  }
  .search-group { display: grid; gap: 6px; }
  .search-label, .search-meta, .search-state { color: hsl(var(--muted-foreground)); font-size: 12px; }
  .search-item, .search-all {
    display: flex; align-items: center; justify-content: space-between; gap: 12px;
    width: 100%; min-height: 40px; padding: 0 12px;
    border-radius: 12px; border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--foreground)); text-align: left; cursor: pointer;
  }
  .search-item:hover, .search-all:hover { background: hsl(0 0% 100% / 0.08); }
  .search-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 700; }
  .search-all { justify-content: center; font-weight: 700; }

  /* ── Topbar right ────────────────────────────── */
  .topbar-right { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }

  .user-chip {
    display: none;
    align-items: center;
    gap: 8px;
    min-height: 40px;
    padding: 0 12px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    text-decoration: none;
  }

  .user-name {
    font-size: 13px;
    font-weight: 700;
  }

  .user-role {
    color: hsl(var(--muted-foreground));
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.12em;
  }

  .icon-btn {
    display: grid; place-items: center; width: 40px; height: 40px;
    border-radius: 14px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--muted-foreground));
    text-decoration: none; cursor: pointer; transition: background .12s;
  }
  .icon-btn:hover { background: hsl(0 0% 100% / 0.1); color: hsl(var(--foreground)); }

  .avatar {
    width: 34px; height: 34px; border-radius: 50%;
    background: hsl(var(--primary)); color: hsl(var(--primary-foreground));
    display: grid; place-items: center; font-size: 12px; font-weight: 800;
  }

  .main { max-width: 1760px; padding: 8px 16px 100px; margin: 0 auto; }
  @media (max-width: 600px) {
    .main { padding-bottom: 80px; }
  }

  /* ── Mobile overlay / drawer ─────────────────── */
  .mobile-overlay {
    position: fixed; inset: 0; z-index: 50;
    background: hsl(0 0% 0% / 0.72); backdrop-filter: blur(8px);
    opacity: 0; pointer-events: none; transition: opacity .18s;
  }
  .mobile-overlay.open { opacity: 1; pointer-events: auto; }

  .mobile-backdrop {
    position: absolute; inset: 0; border: 0; background: transparent; cursor: default;
  }

  .mobile-drawer {
    position: relative; z-index: 1; width: min(22rem, 86vw); height: 100%;
    padding: 16px; border-right: 1px solid hsl(var(--border) / 0.7);
    background: hsl(var(--background)); display: flex; flex-direction: column;
    transform: translateX(-100%); transition: transform .18s;
  }
  .mobile-overlay.open .mobile-drawer { transform: translateX(0); }

  .drawer-head {
    display: flex; justify-content: space-between; align-items: center;
    gap: 12px; margin-bottom: 18px;
  }
  .drawer-brand-wrap { display: flex; align-items: center; gap: 10px; }
  .drawer-brand-icon {
    display: grid; place-items: center; width: 36px; height: 36px; flex-shrink: 0;
    border-radius: 10px; background: hsl(var(--primary)); color: hsl(var(--primary-foreground));
  }
  .drawer-brand { font-size: 18px; font-weight: 700; }
  .drawer-sub { color: hsl(var(--muted-foreground)); font-size: 12px; }

  .drawer-nav { display: grid; gap: 3px; flex: 1; overflow-y: auto; }
  .drawer-nav a {
    display: flex; align-items: center; gap: 12px; padding: 11px 14px;
    border-radius: 16px; border: 1px solid transparent;
    color: hsl(var(--muted-foreground)); text-decoration: none;
    font-size: 14px; font-weight: 600; transition: background .12s, color .12s;
  }
  .drawer-nav a:hover { background: hsl(0 0% 100% / 0.06); color: hsl(var(--foreground)); }
  .drawer-nav a.active {
    background: hsl(0 0% 100% / 0.1); color: hsl(var(--foreground));
    border-color: hsl(0 0% 100% / 0.06);
  }

  /* ── Mobile bottom nav ───────────────────────── */
  .bottom-nav {
    position: fixed; left: 12px; right: 12px; bottom: 12px; z-index: 40;
    display: grid; grid-template-columns: repeat(6, 1fr); gap: 4px; padding: 8px;
    border: 1px solid hsl(0 0% 100% / 0.08); border-radius: 22px;
    background: hsl(0 0% 0% / 0.82); backdrop-filter: blur(18px);
  }

  .bottom-nav a, .bottom-more {
    position: relative; display: flex; align-items: center; justify-content: center;
    height: 46px; border-radius: 18px;
    color: hsl(var(--muted-foreground)); text-decoration: none;
    transition: background .12s, color .12s; background: none; border: none; cursor: pointer;
  }
  .bottom-nav a:hover, .bottom-more:hover {
    background: hsl(0 0% 100% / 0.08); color: hsl(var(--foreground));
  }
  .bottom-nav a.active { background: hsl(0 0% 100% / 0.12); color: hsl(var(--foreground)); }

  .active-dot {
    position: absolute; bottom: 5px; left: 50%; transform: translateX(-50%);
    width: 4px; height: 4px; border-radius: 50%; background: hsl(var(--primary));
  }

  /* ── Desktop overrides ───────────────────────── */
  @media (min-width: 768px) {
    .sidebar { display: flex; }
    .content-wrap { padding-left: 56px; }
    .topbar { padding: 12px 32px; justify-content: center; }
    .searchbar { max-width: 520px; }
    .user-chip { display: inline-flex; }
    .main { padding: 8px 32px 32px; }
    .hamburger, .mobile-overlay, .bottom-nav { display: none !important; }
  }
</style>
