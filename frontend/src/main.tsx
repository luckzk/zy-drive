// SPDX-License-Identifier: MIT
import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import MobileHome from './mobile/MobileHome'
import './tokens.css'
import './mobile/mobile.css'

// Mobile entry: the same Go backend + frontend bundle serves the
// phone home screen when the webview is a phone (coarse pointer —
// what a real iOS/Android webview reports) or when ?mobile=1 is in
// the URL (preview on desktop, and the mobile check script). The
// mobile build needs no mygo.json change and no separate entry
// point; verify on a real phone that the webview reports a coarse
// pointer — if a mygo mobile webview ever reports fine, the
// fallback is a start-URL query in the mobile build config.
const params = new URLSearchParams(window.location.search)
const isMobileHome = params.has('mobile') || window.matchMedia('(pointer: coarse)').matches

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>{isMobileHome ? <MobileHome /> : <App />}</React.StrictMode>,
)
