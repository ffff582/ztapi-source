import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { WorkbenchShell } from './WorkbenchShell';

vi.mock('../../auth/session', () => ({
  useAuth: () => ({
    user: { username: 'alice' },
    logout: vi.fn(),
  }),
}));

afterEach(cleanup);

describe('WorkbenchShell navigation', () => {
  it('exposes only text, image, and video workbenches', () => {
    render(<MemoryRouter initialEntries={['/console/workbench/text']}><WorkbenchShell /></MemoryRouter>);

    const workbenchGroup = screen.getByRole('group', { name: '工作台能力' });
    expect(within(workbenchGroup).getByRole('link', { name: '文本' })).toHaveAttribute('href', '/console/workbench/text');
    expect(within(workbenchGroup).getByRole('link', { name: '图像' })).toHaveAttribute('href', '/console/workbench/image');
    expect(within(workbenchGroup).getByRole('link', { name: '视频' })).toHaveAttribute('href', '/console/workbench/video');
    expect(within(workbenchGroup).queryByRole('link', { name: '音频' })).toBeNull();
  });

  it('offers a direct switch back to the console', () => {
    render(<MemoryRouter initialEntries={['/console/workbench/text']}><WorkbenchShell /></MemoryRouter>);

    const productMode = screen.getByRole('group', { name: '产品模式' });
    expect(within(productMode).getByRole('link', { name: '控制台' })).toHaveAttribute('href', '/console');
    expect(within(productMode).getByRole('link', { name: '工作台' })).toHaveAttribute('href', '/console/workbench/text');
  });
});
