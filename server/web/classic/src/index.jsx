/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import React from 'react';
import ReactDOM from 'react-dom/client';
import { selectApplication } from './ztapi/applicationSelection.js';

export { selectApplication } from './ztapi/applicationSelection.js';

async function renderAdminApplication(rootElement) {
  const { default: AdminApp } = await import('./ztapi/AdminApp.jsx');
  ReactDOM.createRoot(rootElement).render(
    <React.StrictMode>
      <AdminApp />
    </React.StrictMode>,
  );
}

async function renderLegacyApplication(rootElement) {
  const { renderLegacyApplication: renderLegacy } =
    await import('./legacy-entry.jsx');
  renderLegacy(rootElement);
}

const rootElement = document.getElementById('root');
if (rootElement) {
  // Keep this condition compile-time visible so admin builds can exclude the
  // legacy application graph instead of merely hiding it at runtime.
  if (import.meta.env.VITE_ZTAPI_ADMIN_APP === 'true') {
    renderAdminApplication(rootElement);
  } else {
    renderLegacyApplication(rootElement);
  }
}
