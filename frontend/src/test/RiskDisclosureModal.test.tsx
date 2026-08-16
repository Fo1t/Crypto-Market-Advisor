import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import i18n from '../i18n';
import {
  RISK_DISCLOSURE_STORAGE_KEY,
  RISK_DISCLOSURE_VERSION,
  RiskDisclosureModal,
} from '../components/RiskDisclosureModal';

describe('RiskDisclosureModal', () => {
  beforeEach(async () => {
    window.localStorage.clear();
    await i18n.changeLanguage('ru');
  });

  afterEach(() => {
    cleanup();
    document.body.style.overflow = '';
  });

  it('requires explicit acknowledgement and persists the current disclosure version', () => {
    const { unmount } = render(<RiskDisclosureModal />);

    expect(screen.getByRole('dialog')).toBeInTheDocument();
    const continueButton = screen.getByRole('button', { name: /понимаю риски/i });
    expect(continueButton).toBeDisabled();

    fireEvent.click(screen.getByRole('checkbox'));
    expect(continueButton).toBeEnabled();
    fireEvent.click(continueButton);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    expect(JSON.parse(window.localStorage.getItem(RISK_DISCLOSURE_STORAGE_KEY) ?? '{}')).toMatchObject({
      version: RISK_DISCLOSURE_VERSION,
    });

    unmount();
    render(<RiskDisclosureModal />);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('switches the warning language without dismissing it', async () => {
    render(<RiskDisclosureModal />);

    fireEvent.click(screen.getByRole('button', { name: 'English' }));
    await waitFor(() => expect(screen.getByText('Important risk disclosure')).toBeInTheDocument());
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
