<script lang="ts">
  import type { ImageItem } from '$lib/images';

  interface Props {
    items: ImageItem[];
  }

  let { items }: Props = $props();
</script>

<div class="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
  {#each items as item (`${item.pageUrl}#${item.dataUri.slice(-32)}`)}
    <figure
      class="border-brutal-border bg-card-surface flex flex-col gap-2 border-[3px] p-2 shadow-[3px_3px_0_var(--brutal-shadow)]"
    >
      <a
        href={item.pageUrl}
        target="_blank"
        rel="noopener noreferrer"
        class="bg-muted-surface flex aspect-square items-center justify-center overflow-hidden"
        title={item.alt || item.pageTitle}
      >
        <img
          src={item.dataUri}
          alt={item.alt || item.pageTitle}
          loading="lazy"
          class="h-full w-full object-cover"
        />
      </a>
      {#if item.alt}
        <figcaption class="font-inter text-text-brand line-clamp-2 text-sm font-semibold">
          {item.alt}
        </figcaption>
      {/if}
      <a
        href={item.pageUrl}
        target="_blank"
        rel="noopener noreferrer"
        class="font-fira text-text-brand-muted hover:text-hister-cyan line-clamp-1 text-xs break-all"
        title={item.pageTitle}
      >
        {item.domain}
      </a>
    </figure>
  {/each}
</div>
