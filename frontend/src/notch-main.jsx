import React from 'react'
import ReactDOM from 'react-dom/client'
import { Notch } from './Notch.jsx'
import { CrashBoundary } from './Crash.jsx'
import { housingOf, showHousing } from './housing.js'

showHousing(document.documentElement, housingOf(window.location.search))

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <CrashBoundary compact>
      <Notch />
    </CrashBoundary>
  </React.StrictMode>,
)
