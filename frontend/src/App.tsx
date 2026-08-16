import { NavLink, Navigate, Route, Routes } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { StatusBar } from './components/StatusBar';
import { LanguageSwitcher } from './components/LanguageSwitcher';
import { DashboardPage } from './pages/DashboardPage';
import { MarketsPage } from './pages/MarketsPage';
import { MarketDetailPage } from './pages/MarketDetailPage';
import { RecommendationsPage } from './pages/RecommendationsPage';
import { PositionsPage } from './pages/PositionsPage';
import { HistoryPage } from './pages/HistoryPage';
import { StatisticsPage } from './pages/StatisticsPage';
import { BacktestingPage } from './pages/BacktestingPage';
import { SettingsPage } from './pages/SettingsPage';
import { NewsPage } from './pages/NewsPage';
import { RiskDisclosureModal } from './components/RiskDisclosureModal';

const NAV = [
  { to: '/dashboard', key: 'nav.dashboard' },
  { to: '/markets', key: 'nav.markets' },
  { to: '/recommendations', key: 'nav.recommendations' },
  { to: '/news', key: 'nav.news' },
  { to: '/positions', key: 'nav.positions' },
  { to: '/history', key: 'nav.history' },
  { to: '/statistics', key: 'nav.statistics' },
  { to: '/backtesting', key: 'nav.backtesting' },
  { to: '/settings', key: 'nav.settings' },
];

export function App() {
  const { t } = useTranslation();

  return (
    <>
      <div className="app">
        <aside className="sidebar">
          <div className="brand">
            <span className="brand__title">{t('app.title')}</span>
            <span className="brand__subtitle">{t('app.subtitle')}</span>
          </div>

          <nav className="nav">
            {NAV.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                className={({ isActive }) => (isActive ? 'nav__link nav__link--active' : 'nav__link')}
              >
                {t(item.key)}
              </NavLink>
            ))}
          </nav>

          <div className="field">
            <span className="field__label">{t('settings.language')}</span>
            <LanguageSwitcher />
          </div>

          <p className="disclaimer">{t('app.disclaimer')}</p>
        </aside>

        <main className="main">
          <StatusBar />
          <div className="content">
            <Routes>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<DashboardPage />} />
              <Route path="/markets" element={<MarketsPage />} />
              <Route path="/markets/:symbol" element={<MarketDetailPage />} />
              <Route path="/recommendations" element={<RecommendationsPage />} />
              <Route path="/news" element={<NewsPage />} />
              <Route path="/positions" element={<PositionsPage />} />
              <Route path="/history" element={<HistoryPage />} />
              <Route path="/statistics" element={<StatisticsPage />} />
              <Route path="/backtesting" element={<BacktestingPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="*" element={<Navigate to="/dashboard" replace />} />
            </Routes>
          </div>
        </main>
      </div>
      <RiskDisclosureModal />
    </>
  );
}
