<script lang="ts">
  import { base } from '$app/paths';

  export interface GalleryPreviewItem {
    alt?: string;
    key?: string;
    hash?: string;
    data_uri?: string;
  }

  interface Props {
    items: GalleryPreviewItem[];
  }

  let { items }: Props = $props();

  function srcFor(item: GalleryPreviewItem): string {
    if (item.key) return `${base}/api/image?key=${encodeURIComponent(item.key)}`;
    if (item.data_uri) return item.data_uri;
    return '';
  }
</script>

<div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
  {#each items as item, i (`${item.key ?? item.hash ?? item.data_uri?.slice(-32) ?? ''}#${i}`)}
    {@const src = srcFor(item)}
    <figure
      class="border-brutal-border bg-card-surface flex flex-col gap-2 border-[3px] p-2 shadow-[3px_3px_0_var(--brutal-shadow)]"
    >
      {#if src}
        <span class="bg-muted-surface flex aspect-square items-center justify-center overflow-hidden">
          <img
            {src}
            alt={item.alt || ''}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </span>
      {:else}
        <span
          class="bg-muted-surface text-text-brand-muted font-inter flex aspect-square items-center justify-center px-2 text-center text-xs"
        >
          Image upload pending
        </span>
      {/if}
      {#if item.alt}
        <figcaption class="font-inter text-text-brand line-clamp-2 text-sm font-semibold">
          {item.alt}
        </figcaption>
      {/if}
    </figure>
  {/each}
</div>
