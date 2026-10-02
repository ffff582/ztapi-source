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
import React, { createContext, useContext, useEffect, useState } from 'react';
import { adminRequest } from './auth/admin-session.js';

// Balances are stored as internal quota units. Customers top up, are billed
// and read prices in U, so the console shows U everywhere and converts at the
// rate the server reports.
export const defaultQuotaPerUnit = 500000;

export function normalizeQuotaPerUnit(value) {
  const unit = Number(value);
  return Number.isFinite(unit) && unit > 0 ? unit : defaultQuotaPerUnit;
}

const QuotaPerUnitContext = createContext(defaultQuotaPerUnit);

export function QuotaPerUnitProvider({ children }) {
  const [perUnit, setPerUnit] = useState(defaultQuotaPerUnit);
  useEffect(() => {
    let active = true;
    adminRequest({ url: '/api/status' })
      .then((status) => {
        if (active) setPerUnit(normalizeQuotaPerUnit(status?.quota_per_unit));
      })
      .catch(() => undefined);
    return () => {
      active = false;
    };
  }, []);
  return (
    <QuotaPerUnitContext.Provider value={perUnit}>
      {children}
    </QuotaPerUnitContext.Provider>
  );
}

export function useQuotaPerUnit() {
  return useContext(QuotaPerUnitContext);
}

export function quotaToU(quota, perUnit = defaultQuotaPerUnit) {
  return Number(quota || 0) / normalizeQuotaPerUnit(perUnit);
}

// Six significant digits keep a fraction of a cent visible without printing a
// meaningless tail; the stored quota stays exact.
export function formatU(quota, perUnit = defaultQuotaPerUnit) {
  const amount = quotaToU(quota, perUnit);
  if (!Number.isFinite(amount)) return '0 U';
  let text = amount === 0 || Math.abs(amount) >= 1e-6
    ? amount.toPrecision(6)
    : amount.toFixed(10);
  if (text.includes('e')) text = amount.toFixed(10);
  const [whole, fraction = ''] = text.split('.');
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const trimmed = fraction.replace(/0+$/, '');
  return `${grouped}${trimmed ? `.${trimmed}` : ''} U`;
}

// An amount an operator types in U becomes whole quota units, so no fraction
// of a unit is silently dropped on the way to the ledger.
export function uToQuota(value, perUnit = defaultQuotaPerUnit) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return Number.NaN;
  return Math.round(amount * normalizeQuotaPerUnit(perUnit));
}
