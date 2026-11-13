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
import { useTranslation } from 'react-i18next';

const LegacyEmbeddedChatUnavailable = () => {
  const { t } = useTranslation();

  return (
    <main
      className='classic-page-fill flex items-center justify-center p-8 text-center'
      role='status'
    >
      <div className='max-w-xl space-y-4'>
        <h1 className='text-xl font-semibold'>
          {t('Legacy embedded chat unavailable')}
        </h1>
        <p>
          {t('The legacy embedded chat cannot automatically read API keys.')}
        </p>
        <p>
          {t(
            'Create or use an API key, then configure it in an external client.',
          )}
        </p>
        <a
          href='/console/token'
          className='semi-button semi-button-primary semi-button-solid'
        >
          {t('Manage tokens')}
        </a>
      </div>
    </main>
  );
};

export default LegacyEmbeddedChatUnavailable;
