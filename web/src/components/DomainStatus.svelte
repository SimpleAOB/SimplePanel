<script>
  // Checks that a domain's DNS reaches this server and that HTTPS works,
  // and says what to do when it doesn't.
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';

  let { host, https = true, serverIp = '' } = $props();
  let res = $state(null);
  let busy = $state(false);
  let failed = $state('');

  async function check() {
    busy = true;
    failed = '';
    try {
      res = await api.get(`/dns/check?host=${encodeURIComponent(host)}&https=${https ? 1 : 0}`);
    } catch (err) {
      failed = err.message;
    } finally {
      busy = false;
    }
  }
  onMount(check);

  let level = $derived.by(() => {
    if (!res) return '';
    if (res.dns !== 'ok' && res.dns !== 'proxied') return 'bad';
    if (https && res.https !== 'ok') return res.https === 'pending' ? 'wait' : 'bad';
    return 'good';
  });
  let label = $derived.by(() => {
    if (!res) return '';
    if (res.dns === 'missing') return 'No DNS record';
    if (res.dns === 'elsewhere') return 'Points at another server';
    if (res.dns === 'unreachable') return 'Not reachable';
    if (https && res.https === 'pending') return 'Waiting for certificate';
    if (https && res.https === 'error') return 'HTTPS not working';
    if (res.dns === 'proxied') return 'Through Cloudflare';
    return https ? 'Working, HTTPS on' : 'Working';
  });
  let message = $derived.by(() => {
    if (!res || level === 'good') return '';
    if (res.dns === 'missing' || res.dns === 'elsewhere') {
      return serverIp ? `${res.dnsMessage} This server's IP address is ${serverIp}.` : res.dnsMessage;
    }
    if (res.dns !== 'ok' && res.dns !== 'proxied') return res.dnsMessage;
    return res.httpsMessage || res.dnsMessage;
  });
</script>

<div class="status">
  {#if busy && !res}
    <span class="dot"></span><span class="faint">Checking DNS…</span>
  {:else if failed}
    <span class="dot"></span><span class="faint">Couldn't check: {failed}</span>
  {:else if res}
    <span class="dot {level}"></span>
    <span class="label {level}">{label}</span>
  {/if}
  <button class="linkish" disabled={busy} onclick={check}>{busy ? 'Checking…' : 'Check again'}</button>
</div>
{#if message}<p class="msg">{message}</p>{/if}

<style>
  .status {
    display: flex;
    align-items: center;
    gap: var(--sp-space-2);
    font-size: var(--sp-text-sm);
    padding-left: 24px;
  }
  .label.good {
    color: var(--sp-success);
  }
  .label.wait {
    color: var(--sp-warning);
  }
  .label.bad {
    color: var(--sp-danger);
  }
  .dot.good {
    background: var(--sp-success);
  }
  .dot.wait {
    background: var(--sp-warning);
  }
  .dot.bad {
    background: var(--sp-danger);
  }
  .linkish {
    font-size: var(--sp-text-xs);
  }
  .msg {
    margin: 0;
    padding-left: 24px;
    font-size: var(--sp-text-sm);
    color: var(--sp-text-muted);
  }
</style>
