// Cron schedule helpers for the cron jobs UI.

export const schedulePresets = [
  ['* * * * *', 'Every minute'],
  ['*/5 * * * *', 'Every 5 minutes'],
  ['*/15 * * * *', 'Every 15 minutes'],
  ['0 * * * *', 'Every hour'],
  ['0 */6 * * *', 'Every 6 hours'],
  ['0 3 * * *', 'Every day at 03:00'],
  ['0 9 * * 1-5', 'Weekdays at 09:00'],
  ['0 3 * * 0', 'Every Sunday at 03:00'],
  ['0 3 1 * *', 'First of the month at 03:00'],
];

const aliases = { '@hourly': '0 * * * *', '@daily': '0 3 * * *', '@weekly': '0 3 * * 0', '@monthly': '0 3 1 * *' };
const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
const pad = (n) => String(n).padStart(2, '0');
const num = (s) => /^\d+$/.test(s);

// describeSchedule turns common cron expressions into words, and returns
// the expression itself for anything unusual.
export function describeSchedule(expr) {
  const e = aliases[(expr || '').trim().toLowerCase()] || (expr || '').trim();
  const preset = schedulePresets.find((p) => p[0] === e);
  if (preset) return preset[1];
  const f = e.split(/\s+/);
  if (f.length !== 5) return e || '—';
  const [mi, h, dom, mon, dow] = f;
  const step = (s) => (s.startsWith('*/') && num(s.slice(2)) ? Number(s.slice(2)) : 0);
  if (step(mi) && h === '*' && dom === '*' && mon === '*' && dow === '*') return `Every ${step(mi)} minutes`;
  if (num(mi) && h === '*' && dom === '*' && mon === '*' && dow === '*') return `Every hour at :${pad(mi)}`;
  if (num(mi) && step(h) && dom === '*' && mon === '*' && dow === '*') return `Every ${step(h)} hours at :${pad(mi)}`;
  if (num(mi) && num(h) && mon === '*') {
    const at = `${pad(h)}:${pad(mi)}`;
    if (dom === '*' && dow === '*') return `Every day at ${at}`;
    if (dom === '*' && num(dow)) return `Every ${days[Number(dow)]} at ${at}`;
    if (dom === '*' && dow === '1-5') return `Weekdays at ${at}`;
    if (num(dom) && dow === '*') return `Day ${dom} of every month at ${at}`;
  }
  return e;
}
