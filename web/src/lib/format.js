// Formatting helpers for sizes, rates and times.

export function bytes(n, digits = 1) {
  if (n == null || isNaN(n)) return '–';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = Math.abs(n);
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : v >= 100 ? 0 : digits)} ${units[i]}`;
}

export const rate = (n) => bytes(n) + '/s';

export function pct(n, digits = 0) {
  if (n == null || isNaN(n)) return '–';
  if (n === 0) return '0%';
  return `${n.toFixed(n < 10 && digits === 0 ? 1 : digits)}%`;
}

export function dateTime(unix) {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

export function clock(unix, withDay = false) {
  const d = new Date(unix * 1000);
  return withDay
    ? d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

// Schedule presets for backups. The value is a cron expression.
export const schedulePresets = [
  ['@hourly', 'Every hour'],
  ['0 3 * * *', 'Every day at 3:00'],
  ['0 3 * * 0', 'Every Sunday at 3:00'],
  ['0 3 1 * *', 'On the 1st of each month at 3:00'],
];

export function describeSchedule(expr) {
  const p = schedulePresets.find(([v]) => v === expr);
  if (p) return p[1];
  if (expr === '@daily') return 'Every day at 3:00';
  if (expr === '@weekly') return 'Every Sunday at 3:00';
  if (expr === '@monthly') return 'On the 1st of each month at 3:00';
  return `Cron: ${expr}`;
}
