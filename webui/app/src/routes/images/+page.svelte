<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { onMount } from 'svelte';
  import { fetchImages, type ImageItem } from '$lib/images';
  import { StatusMessage } from '$lib/components';
  import ImagesSection from '$lib/components/ImagesSection.svelte';
  import { PageHeader } from '@hister/components';
  import { Input } from '@hister/components/ui/input';
  import { Search } from '@lucide/svelte';

  let items: ImageItem[] = $state([]);
  let loading = $state(true);
  let error = $state('');
  let filter = $state('');

  const filtered = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return items;
    return items.filter(
      (item) =>
        item.alt.toLowerCase().includes(q) ||
        item.domain.toLowerCase().includes(q) ||
        item.pageTitle.toLowerCase().includes(q),
    );
  });

  onMount(async () => {
    try {
      items = await fetchImages();
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load images';
    } finally {
      loading = false;
    }
  });
</script>

<svelte:head>
  <title>Hister - Images</title>
</svelte:head>

<header class="border-brutal-border bg-card-surface shrink-0 border-b-[3px] px-3 py-3 md:px-6">
  <div class="flex min-w-0 flex-wrap items-center gap-3 md:gap-4">
    <PageHeader color="hister-cyan" size="sm" class="min-w-0 shrink-0" truncate>
      Images
    </PageHeader>
    {#if !loading && !error}
      <span class="font-fira text-text-brand-muted text-xs">
        {filtered.length} images
      </span>
    {/if}
    <div class="relative ml-auto w-full max-w-xs">
      <Search
        class="text-text-brand-muted pointer-events-none absolute top-1/2 left-2 size-4 -translate-y-1/2"
      />
      <Input
        type="search"
        placeholder="Filter by description or site..."
        bind:value={filter}
        class="pl-8"
        aria-label="Filter images"
      />
    </div>
  </div>
</header>

<div class="min-h-0 flex-1 overflow-y-auto">
  {#if loading}
    <StatusMessage message="Loading images..." type="loading" />
  {:else if error}
    <StatusMessage message={error} type="error" class="mx-3 mt-4 md:mx-6" />
  {:else if items.length === 0}
    <StatusMessage message="No images yet" type="empty" />
  {:else if filtered.length === 0}
    <StatusMessage message="No images match this filter" type="empty" />
  {:else}
    <div class="mx-auto w-full max-w-6xl px-3 py-3 md:px-6 md:py-5">
      <ImagesSection items={filtered} />
    </div>
  {/if}
</div>
