import {
  createContext,
  useContext,
  useMemo,
  useState,
  type PropsWithChildren,
} from 'react';
import { KeyCreatedDialog } from './KeyCreatedDialog';

interface OneTimeSecretContextValue {
  secretPending: boolean;
  showOneTimeSecret: (secret: string) => void;
}

const OneTimeSecretContext = createContext<OneTimeSecretContextValue | null>(
  null,
);

export function OneTimeSecretProvider({ children }: PropsWithChildren) {
  const [secret, setSecret] = useState<string | null>(null);
  const value = useMemo(
    () => ({
      secretPending: secret !== null,
      showOneTimeSecret: setSecret,
    }),
    [secret],
  );

  return (
    <OneTimeSecretContext.Provider value={value}>
      {children}
      {secret !== null && (
        <KeyCreatedDialog
          plaintextKey={secret}
          onAcknowledge={() => setSecret(null)}
        />
      )}
    </OneTimeSecretContext.Provider>
  );
}

export function useOneTimeSecret() {
  const value = useContext(OneTimeSecretContext);
  if (value === null) {
    throw new Error('useOneTimeSecret must be used within OneTimeSecretProvider');
  }
  return value;
}
