const control = document.querySelector('#theme');
function applyTheme(theme) {
  if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
  if (control) control.value = theme;
}
let saved = 'auto';
try { saved = localStorage.getItem('castaway-theme') || 'auto'; } catch {}
if (!['auto', 'light', 'dark'].includes(saved)) saved = 'auto';
applyTheme(saved);
control?.addEventListener('change', () => {
  applyTheme(control.value);
  try { localStorage.setItem('castaway-theme', control.value); } catch {}
});
window.addEventListener('storage', event => {
  if (event.key === 'castaway-theme') applyTheme(['light', 'dark'].includes(event.newValue) ? event.newValue : 'auto');
});
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
