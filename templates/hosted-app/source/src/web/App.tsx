import { useEffect, useState } from 'react';

type Preferences = { version: 1; selected_tab: 'overview' | 'preferences'; period: '1h' | '24h' | '7d' };
const defaults: Preferences = { version: 1, selected_tab: 'overview', period: '24h' };

// The wrapper is also callable by the literal-title server-rendering test.
export function App({ title }: { title: string }) {
  return <main><header><h1>{title}</h1><p className="muted">A small example with shared, saved display settings.</p></header><DisplayPreferences /></main>;
}

function DisplayPreferences() {
  const [settings, setSettings] = useState<Preferences>(defaults);
  const [busy, setBusy] = useState(true);
  const [status, setStatus] = useState('Loading preferences…');
  useEffect(() => {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 10000);
    let active = true;
    void (async () => {
      try {
        const response = await fetch('/api/preferences', { credentials: 'omit', signal: controller.signal });
        if (response.status !== 200) throw new Error('read failed');
        const value = await response.json();
        if (value?.version !== 1 || !['overview', 'preferences'].includes(value.selected_tab) || !['1h', '24h', '7d'].includes(value.period)) throw new Error('invalid record');
        if (active) { setSettings({ version: 1, selected_tab: value.selected_tab, period: value.period }); setStatus('Preferences loaded.'); }
      } catch { if (active) setStatus('Could not load preferences. Defaults shown; save only when ready.'); }
      finally { clearTimeout(timeout); if (active) setBusy(false); }
    })();
    return () => { active = false; clearTimeout(timeout); controller.abort(); };
  }, []);
  function edit(next: Preferences) { setSettings(next); setStatus('Unsaved changes.'); }
  async function save() {
    if (busy) return;
    setBusy(true); setStatus('Saving…');
    try {
      const response = await fetch('/api/preferences', { method: 'POST', credentials: 'omit',
        headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(settings), signal: AbortSignal.timeout(10000) });
      if (response.status !== 204) throw new Error('write not acknowledged');
      setStatus('Preferences saved.');
    } catch { setStatus('Save not confirmed. Your changes remain here; check saved settings before saving again.'); }
    finally { setBusy(false); }
  }
  return <>
    <nav aria-label="Display views">
      {(['overview', 'preferences'] as const).map((tab) => <button key={tab} type="button" disabled={busy}
        aria-pressed={settings.selected_tab === tab} onClick={() => edit({ ...settings, selected_tab: tab })}>
        {tab === 'overview' ? 'Overview' : 'Preferences'}</button>)}
    </nav>
    <section className="controls" aria-label="Display settings">
      <label htmlFor="period">Period</label>
      <select id="period" value={settings.period} disabled={busy}
        onChange={(event) => edit({ ...settings, period: event.target.value as Preferences['period'] })}>
        <option value="1h">Last hour</option><option value="24h">Last 24 hours</option><option value="7d">Last 7 days</option>
      </select>
      <button type="button" disabled={busy} onClick={() => void save()}>Save</button>
    </section>
    <p role="status" aria-live="polite" aria-atomic="true">{status}</p>
    {settings.selected_tab === 'overview' ? <section aria-labelledby="overview-heading">
      <h2 id="overview-heading">Overview</h2><p className="muted">Illustrative values for {settings.period}; these are example data.</p>
      <div className="cards">{[['Projects', '3', 'Ideas ready to explore'], ['Activity', '12', 'Example completed tasks'], ['Availability', '98%', 'Illustrative service reading']].map(([name, value, detail]) =>
        <article className="card" key={name}><h3>{name}</h3><p className="metric">{value}</p><p className="muted">{detail}</p></article>)}</div>
      <div className="table-scroll" tabIndex={0} role="region" aria-label="Scrollable example activity">
        <table><caption>Example activity</caption><thead><tr><th scope="col">Project</th><th scope="col">Description</th><th scope="col">Status</th><th scope="col">Period</th></tr></thead>
          <tbody>{['Garden planner', 'Reading list', 'Weekly notes'].map((name) => <tr key={name}><th scope="row">{name}</th><td>Synthetic demonstration entry</td><td>Ready</td><td>{settings.period}</td></tr>)}</tbody></table>
      </div>
    </section> : <section aria-labelledby="preferences-heading"><h2 id="preferences-heading">Preferences</h2>
      <p>Choose a view and period, then Save. Acknowledged settings are shared by fresh clients of this app.</p>
      <p className="muted">Settings live in the app database and survive service restarts and replacement builds.</p></section>}
  </>;
}
