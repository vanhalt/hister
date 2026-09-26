<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { Badge } from '@hister/components/ui/badge';
  import { Button } from '@hister/components/ui/button';
  import { Separator } from '@hister/components/ui/separator';
  import { Mail, Phone, MessageCircle, Share2, Copy, Check, MapPin, Clock, Link2, Fingerprint } from '@lucide/svelte';
  import type { ContactInfo } from '$lib/types';

  let { contacts }: { contacts: ContactInfo } = $props();

  let copied = $state('');

  const emails = $derived(contacts.emails ?? []);
  const phones = $derived(contacts.phones ?? []);
  const whatsapp = $derived(contacts.whatsapp ?? []);
  const socials = $derived(contacts.socials ?? []);
  const ruts = $derived(contacts.ruts ?? []);
  const addresses = $derived(contacts.addresses ?? []);
  const hours = $derived(contacts.hours ?? []);
  const links = $derived(contacts.links ?? []);
  const total = $derived(
    emails.length +
      phones.length +
      whatsapp.length +
      socials.length +
      ruts.length +
      addresses.length +
      hours.length +
      links.length,
  );

  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      copied = value;
      setTimeout(() => {
        if (copied === value) copied = '';
      }, 1500);
    } catch {
      // clipboard unavailable; ignore
    }
  }

  function copyHref(kind: string, value: string): string {
    if (kind === 'email') return `mailto:${value}`;
    return `tel:${value}`;
  }
</script>

{#if total === 0}
  <p class="font-inter text-text-brand-muted text-xs italic">No contact information found.</p>
{:else}
  <section aria-label="Extracted contacts" class="not-prose font-inter space-y-4">
    {#if emails.length}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Mail class="text-hister-teal size-4" /> Emails ({emails.length})
        </h3>
        <ul class="space-y-1.5">
          {#each emails as e (e.value)}
            <li class="flex flex-wrap items-center gap-1.5 text-xs">
              <a
                href={copyHref('email', e.value)}
                class="text-hister-teal break-all hover:underline">{e.value}</a
              >
              {#if e.obfuscated}
                <Badge variant="outline" class="text-[10px]">de-obfuscated</Badge>
              {/if}
              <Badge variant="secondary" class="text-[10px]">{e.confidence}</Badge>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-text-brand-muted hover:text-text-brand"
                onclick={() => copy(e.value)}
                title="Copy email"
                aria-label="Copy {e.value}"
              >
                {#if copied === e.value}
                  <Check class="size-3.5" />
                {:else}
                  <Copy class="size-3.5" />
                {/if}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if phones.length}
      {#if emails.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Phone class="text-hister-teal size-4" /> Phones ({phones.length})
        </h3>
        <ul class="space-y-1.5">
          {#each phones as p (p.normalized || p.value)}
            <li class="flex flex-wrap items-center gap-1.5 text-xs">
              <a href={copyHref('tel', p.normalized || p.value)} class="hover:underline"
                >{p.value}</a
              >
              <Badge variant="secondary" class="text-[10px]">{p.confidence}</Badge>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-text-brand-muted hover:text-text-brand"
                onclick={() => copy(p.normalized || p.value)}
                title="Copy phone"
                aria-label="Copy {p.value}"
              >
                {#if copied === (p.normalized || p.value)}
                  <Check class="size-3.5" />
                {:else}
                  <Copy class="size-3.5" />
                {/if}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if whatsapp.length}
      {#if emails.length || phones.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <MessageCircle class="text-hister-teal size-4" /> WhatsApp ({whatsapp.length})
        </h3>
        <ul class="space-y-1.5">
          {#each whatsapp as w (w.normalized || w.value)}
            <li class="flex flex-wrap items-center gap-1.5 text-xs">
              <a
                href="https://wa.me/{(w.normalized || w.value).replace(/^\+/, '')}"
                target="_blank"
                rel="noopener noreferrer"
                class="text-hister-teal hover:underline">{w.value}</a
              >
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-text-brand-muted hover:text-text-brand"
                onclick={() => copy(w.normalized || w.value)}
                title="Copy WhatsApp number"
                aria-label="Copy {w.value}"
              >
                {#if copied === (w.normalized || w.value)}
                  <Check class="size-3.5" />
                {:else}
                  <Copy class="size-3.5" />
                {/if}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if socials.length}
      {#if emails.length || phones.length || whatsapp.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Share2 class="text-hister-teal size-4" /> Social ({socials.length})
        </h3>
        <nav class="flex flex-wrap gap-1.5" aria-label="Social profiles">
          {#each socials as s (s.network + s.url)}
            <Badge variant="outline" class="text-[11px]">
              <a
                href={s.url}
                target="_blank"
                rel="noopener noreferrer"
                class="hover:underline"
                title={s.handle ? `${s.network}: ${s.handle}` : s.network}
              >
                {s.network}{s.handle ? `/${s.handle}` : ''}
              </a>
            </Badge>
          {/each}
        </nav>
      </div>
    {/if}

    {#if ruts.length}
      {#if emails.length || phones.length || whatsapp.length || socials.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Fingerprint class="text-hister-teal size-4" /> RUT ({ruts.length})
        </h3>
        <ul class="space-y-1.5">
          {#each ruts as r (r.normalized || r.value)}
            <li class="flex flex-wrap items-center gap-1.5 text-xs">
              <code>{r.normalized || r.value}</code>
              <Badge variant="secondary" class="text-[10px]">verified</Badge>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-text-brand-muted hover:text-text-brand"
                onclick={() => copy(r.normalized || r.value)}
                title="Copy RUT"
                aria-label="Copy {r.value}"
              >
                {#if copied === (r.normalized || r.value)}
                  <Check class="size-3.5" />
                {:else}
                  <Copy class="size-3.5" />
                {/if}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if addresses.length}
      {#if emails.length || phones.length || whatsapp.length || socials.length || ruts.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <MapPin class="text-hister-teal size-4" /> Addresses ({addresses.length})
        </h3>
        <ul class="space-y-1.5">
          {#each addresses as a (a.value)}
            <li class="flex flex-wrap items-center gap-1.5 text-xs">
              <span>{a.value}</span>
              <Badge variant="secondary" class="text-[10px]">{a.confidence}</Badge>
              <Button
                variant="ghost"
                size="icon-sm"
                class="text-text-brand-muted hover:text-text-brand"
                onclick={() => copy(a.value)}
                title="Copy address"
                aria-label="Copy {a.value}"
              >
                {#if copied === a.value}
                  <Check class="size-3.5" />
                {:else}
                  <Copy class="size-3.5" />
                {/if}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if hours.length}
      {#if emails.length || phones.length || whatsapp.length || socials.length || ruts.length || addresses.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Clock class="text-hister-teal size-4" /> Hours ({hours.length})
        </h3>
        <ul class="space-y-1.5">
          {#each hours as h (h.value)}
            <li class="text-xs">{h.value}</li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if links.length}
      {#if emails.length || phones.length || whatsapp.length || socials.length || ruts.length || addresses.length || hours.length}<Separator />{/if}
      <div>
        <h3
          class="font-outfit text-text-brand mb-2 flex items-center gap-1.5 text-sm font-bold tracking-wide uppercase"
        >
          <Link2 class="text-hister-teal size-4" /> Links ({links.length})
        </h3>
        <nav class="flex flex-wrap gap-1.5" aria-label="Contact links">
          {#each links as l (l.kind + l.url)}
            <Badge variant="outline" class="text-[11px]">
              <a href={l.url} target="_blank" rel="noopener noreferrer" class="hover:underline">
                {l.kind === 'map' ? 'Map' : 'Contact page'}
              </a>
            </Badge>
          {/each}
        </nav>
      </div>
    {/if}
  </section>
{/if}
