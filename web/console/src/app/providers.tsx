import type { PropsWithChildren } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvider } from '../auth/session';
import { OneTimeSecretProvider } from '../features/keys/OneTimeSecretProvider';
import { LocaleProvider } from '../i18n/locale';

const queryClient = new QueryClient();

export function AppProviders({ children }: PropsWithChildren) {
  return (
    <LocaleProvider>
      <QueryClientProvider client={queryClient}>
        <OneTimeSecretProvider>
          <AuthProvider>{children}</AuthProvider>
        </OneTimeSecretProvider>
      </QueryClientProvider>
    </LocaleProvider>
  );
}
