// Resolve the theme before the first paint.
//
// This used to be an inline <script> in index.html. It lives in its own file so
// the document carries no inline JavaScript, which is what lets the desktop
// shell serve the console under `script-src 'self'` instead of having to allow
// 'unsafe-inline' for one snippet.
//
// It is a classic (non-module) script on purpose: a parser blocking script in the
// head runs before the body is painted, which is the whole point. Deferring it to
// Vue's mount would show a dark frame first on a machine set to light.
;(function () {
  try {
    var stored = window.localStorage.getItem('agenthub.theme')
    document.documentElement.dataset.theme =
      stored === 'light' || stored === 'dark'
        ? stored
        : window.matchMedia('(prefers-color-scheme: light)').matches
          ? 'light'
          : 'dark'
  } catch (error) {
    document.documentElement.dataset.theme = 'dark'
  }
})()
