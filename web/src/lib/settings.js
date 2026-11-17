// Settings sections: [key, label, icon]. Admin-only sections are hidden
// from other roles.
export function settingsSections(admin) {
  const all = [
    ['general', 'General', 'globe', true],
    ['users', 'Users', 'users', true],
    ['backups', 'Backups', 'archive', true],
    ['notifications', 'Notifications', 'bell', true],
    ['monitoring', 'Monitoring', 'chart', true],
    ['ai', 'Connect an AI', 'sparkles', false],
    ['account', 'Account', 'user', false],
  ];
  return all.filter((s) => admin || !s[3]).map(([k, l, i]) => [k, l, i]);
}
