// Copies text to the clipboard. navigator.clipboard only exists on HTTPS
// (and localhost), and a fresh install is reached over http://IP:3000, so
// fall back to the older execCommand copy from a hidden textarea.
export async function copyText(text) {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      /* fall through */
    }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.top = '-1000px';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  if (!ok) throw new Error("Couldn't copy. Select the text and press Ctrl+C.");
}
