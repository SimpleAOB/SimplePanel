<script>
  let { size = 32 } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
  <rect width="32" height="32" rx="8" fill="var(--sp-accent)" />
  <path d="M9 11h14M9 16h14M9 21h9" stroke="var(--sp-accent-contrast)" stroke-width="2.5" stroke-linecap="round" />
</svg>
