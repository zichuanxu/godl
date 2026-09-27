import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'

type Status = 'queued' | 'running' | 'paused' | 'completed' | 'failed'

type Download = {
  id: string
  url: string
  destination: string
  status: Status
  completed: number
  total: number
  error?: string
  createdAt: string
  updatedAt: string
}

const SERVICE_URL = 'http://127.0.0.1:51000'

function App() {
  const [downloads, setDownloads] = useState<Download[]>([])
  const [url, setUrl] = useState('')
  const [destination, setDestination] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [serviceError, setServiceError] = useState('')
  const [formError, setFormError] = useState('')
  const [connection, setConnection] = useState<'connecting' | 'online' | 'offline'>('connecting')

  const refresh = useCallback(async () => {
    try {
      const response = await fetch(`${SERVICE_URL}/v1/downloads`)
      if (!response.ok) throw new Error(`Service returned ${response.status}`)
      const items = (await response.json()) as Download[]
      setDownloads(items)
      setServiceError('')
      setConnection('online')
    } catch (error) {
      setConnection('offline')
      setServiceError(error instanceof Error ? error.message : 'Unable to connect to godl service')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    let source: EventSource | undefined
    let retryTimer: number | undefined
    let closed = false

    const connect = () => {
      if (closed) return
      setConnection('connecting')
      source = new EventSource(`${SERVICE_URL}/v1/events`)
      source.onopen = () => {
        setConnection('online')
        setServiceError('')
      }
      source.addEventListener('updated', (event) => {
        const item = JSON.parse((event as MessageEvent).data) as Download
        setDownloads((current) => {
          const exists = current.some((download) => download.id === item.id)
          return exists ? current.map((download) => download.id === item.id ? item : download) : [...current, item]
        })
      })
      source.addEventListener('progress', (event) => {
        const item = JSON.parse((event as MessageEvent).data) as Download
        setDownloads((current) => current.map((download) => download.id === item.id ? item : download))
      })
      source.onerror = () => {
        source?.close()
        setConnection('offline')
        retryTimer = window.setTimeout(connect, 2000)
      }
    }

    connect()
    return () => {
      closed = true
      source?.close()
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
    }
  }, [])

  async function addDownload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setFormError('')
    if (!url.trim() || !destination.trim()) {
      setFormError('URL and destination are required.')
      return
    }
    setSubmitting(true)
    try {
      const response = await fetch(`${SERVICE_URL}/v1/downloads`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: url.trim(), destination: destination.trim() }),
      })
      if (!response.ok) throw new Error(`Service returned ${response.status}`)
      const item = (await response.json()) as Download
      setDownloads((current) => [...current, item])
      setUrl('')
      setDestination('')
      setConnection('online')
    } catch (error) {
      setFormError(error instanceof Error ? error.message : 'Unable to queue download')
    } finally {
      setSubmitting(false)
    }
  }

  async function pauseDownload(id: string) {
    try {
      const response = await fetch(`${SERVICE_URL}/v1/downloads/${encodeURIComponent(id)}:pause`, { method: 'POST' })
      if (!response.ok) throw new Error(`Service returned ${response.status}`)
      setDownloads((current) => current.map((download) => download.id === id ? { ...download, status: 'paused' } : download))
    } catch (error) {
      setServiceError(error instanceof Error ? error.message : 'Unable to pause download')
    }
  }

  const activeCount = useMemo(() => downloads.filter((download) => download.status === 'running').length, [downloads])

  return (
    <main className="shell">
      <header className="topbar">
        <div>
          <p className="eyebrow">DOWNLOAD MANAGER</p>
          <h1>godl</h1>
        </div>
        <div className="service-state" aria-live="polite">
          <span className={`state-dot state-${connection}`} aria-hidden="true" />
          <span>{connection === 'online' ? 'Service online' : connection === 'connecting' ? 'Connecting' : 'Service offline'}</span>
        </div>
      </header>

      <section className="summary" aria-label="Download summary">
        <div><span className="summary-label">Total</span><strong>{downloads.length}</strong></div>
        <div><span className="summary-label">Active</span><strong>{activeCount}</strong></div>
        <button className="refresh" type="button" onClick={() => void refresh()}>Refresh</button>
      </section>

      <section className="queue-form" aria-labelledby="queue-heading">
        <div className="section-heading">
          <div><p className="eyebrow">NEW TRANSFER</p><h2 id="queue-heading">Add a download</h2></div>
          <span className="shortcut">Enter to queue</span>
        </div>
        <form onSubmit={addDownload}>
          <div className="field-grid">
            <label>Source URL<input value={url} onChange={(event) => setUrl(event.target.value)} type="url" placeholder="https://example.com/file.zip" autoComplete="url" /></label>
            <label>Save to<input value={destination} onChange={(event) => setDestination(event.target.value)} type="text" placeholder="/Users/name/Downloads/file.zip" /></label>
            <button className="primary" type="submit" disabled={submitting}>{submitting ? 'Queueing…' : 'Queue download'}</button>
          </div>
          {formError && <p className="error-message" role="alert">{formError}</p>}
        </form>
      </section>

      <section className="downloads" aria-labelledby="downloads-heading">
        <div className="section-heading"><div><p className="eyebrow">QUEUE</p><h2 id="downloads-heading">Downloads</h2></div></div>
        {serviceError && <div className="notice" role="alert">{serviceError}<button type="button" onClick={() => void refresh()}>Retry</button></div>}
        {loading ? <div className="empty-state" aria-busy="true">Loading downloads…</div> : downloads.length === 0 ? <div className="empty-state">No downloads yet. Add a URL above to get started.</div> : (
          <div className="table-wrap"><table><caption className="sr-only">Queued downloads</caption><thead><tr><th scope="col">File</th><th scope="col">Status</th><th scope="col">Progress</th><th scope="col">Updated</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead><tbody>{downloads.map((download) => {
            const percent = download.total > 0 ? Math.min(100, download.completed / download.total * 100) : 0
            return <tr key={download.id}><td><strong className="filename">{download.destination.split(/[\\/]/).pop() || download.destination}</strong><span className="source">{download.url}</span></td><td><span className={`status status-${download.status}`}>{download.status}</span>{download.error && <span className="row-error">{download.error}</span>}</td><td><div className="progress-cell"><div className="progress-track"><span style={{ width: `${percent}%` }} /></div><span>{download.total > 0 ? `${percent.toFixed(0)}%` : '—'}</span></div></td><td>{new Date(download.updatedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</td><td>{(download.status === 'queued' || download.status === 'running') && <button className="pause" type="button" onClick={() => void pauseDownload(download.id)}>Pause</button>}</td></tr>
          })}</tbody></table></div>
        )}
      </section>
    </main>
  )
}

createRoot(document.getElementById('root')!).render(<App />)
