import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { RouterProvider } from 'react-router-dom';
import { AppProviders } from './app/providers';
import { createZTAPIRouter } from './app/router';
import './brand/tokens.css';

const rootElement = document.getElementById('root');

if (rootElement === null) {
  throw new Error('Root element not found.');
}

createRoot(rootElement).render(
  <StrictMode>
    <AppProviders>
      <RouterProvider router={createZTAPIRouter()} />
    </AppProviders>
  </StrictMode>,
);
