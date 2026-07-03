// Request/response + reactive client for the Shoka /ws/ui WebSocket.
//
// Shoka serves all read data (projects, file tree, file content) over /ws/ui as
// request/response messages — there is no REST read API. The protocol has no
// request IDs, but the server's read loop processes one message at a time and
// writes exactly one response per request in order (internal/ui/manager.go), so
// a single FIFO queue of pending requests correlates responses correctly.
//
// NOTIFY frames (server push, {type:"NOTIFY", payload:Event}) are distinguished
// by their type and dispatched to a handler *before* the FIFO match step, so a
// NOTIFY never consumes a pending request slot (session-2 constraint §2).
//
// Reconnect (session 2): on an unexpected close, in-flight requests reject and
// the client reconnects with exponential backoff + jitter (cap 30s, infinite
// retries). Connection state is observable for the status-bar indicator.

const WS_OPEN = 1
const BACKOFF_CAP_MS = 30_000

// A response frame, type included. SAVE_FILE callers must see the type to tell a
// SAVE_ACK from a CONFLICT (both are non-ERROR responses); `request` below hides
// it for the common payload-only case.
export interface WsFrame {
  type: string
  payload: unknown
}

interface Pending {
  resolve: (frame: WsFrame) => void
  reject: (err: Error) => void
}

// Minimal structural type for a WebSocket, so tests can inject a fake.
export interface WsLike {
  send(data: string): void
  close(): void
  readyState: number
  onopen: (() => void) | null
  onclose: (() => void) | null
  onerror: (() => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
}

export type WsFactory = (url: string) => WsLike

export type ConnStatus =
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'disconnected'

export interface ConnState {
  status: ConnStatus
  attempt: number // failed reconnect attempts since last connected
  connectedSince: number | null // ms epoch, while connected
  lastConnectedAt: number | null // ms epoch of the most recent open ("last update")
  retryAt: number | null // ms epoch of the next scheduled reconnect attempt
}

export interface WsClientOptions {
  factory?: WsFactory
  now?: () => number
  random?: () => number
  // Schedule fn after ms; returns a canceller. Injectable for time-mocked tests.
  schedule?: (fn: () => void, ms: number) => () => void
}

// Backoff base (pre-jitter) for the Nth reconnect attempt: 0, 1s, 2s, 4s, 8s,
// 16s, then capped at 30s. attempt is 0-based (0 = first retry, fires ~immediately).
export function backoffBaseMs(attempt: number): number {
  if (attempt <= 0) return 0
  return Math.min(BACKOFF_CAP_MS, 1000 * 2 ** (attempt - 1))
}

export class WsRequestClient {
  private ws: WsLike | null = null
  private connecting = false
  private closedByUser = false
  private reconnectCancel: (() => void) | null = null
  private readonly outbox: string[] = []
  private readonly pending: Pending[] = []
  private notifyHandler: ((payload: unknown) => void) | null = null
  private denyHandler: ((payload: unknown) => void) | null = null

  private readonly factory: WsFactory
  private readonly now: () => number
  private readonly random: () => number
  private readonly schedule: (fn: () => void, ms: number) => () => void

  private readonly stateListeners = new Set<(s: ConnState) => void>()
  private state: ConnState = {
    status: 'connecting',
    attempt: 0,
    connectedSince: null,
    lastConnectedAt: null,
    retryAt: null,
  }

  constructor(
    private readonly url: string,
    opts: WsClientOptions = {},
  ) {
    this.factory =
      opts.factory ?? ((u) => new WebSocket(u) as unknown as WsLike)
    this.now = opts.now ?? (() => Date.now())
    this.random = opts.random ?? Math.random
    this.schedule =
      opts.schedule ??
      ((fn, ms) => {
        const t = setTimeout(fn, ms)
        return () => clearTimeout(t)
      })
  }

  // --- connection state observation ---------------------------------------

  getState(): ConnState {
    return this.state
  }

  subscribeState(listener: (s: ConnState) => void): () => void {
    this.stateListeners.add(listener)
    return () => this.stateListeners.delete(listener)
  }

  private setState(patch: Partial<ConnState>): void {
    this.state = { ...this.state, ...patch }
    for (const l of this.stateListeners) l(this.state)
  }

  // --- NOTIFY dispatch -----------------------------------------------------

  /** Route inbound NOTIFY payloads to this handler (set by the app layer). */
  setNotifyHandler(fn: (payload: unknown) => void): void {
    this.notifyHandler = fn
  }

  /**
   * Route inbound PERMISSION_DENIED payloads to this handler (set by the app layer
   * to surface a non-fatal toast). PERMISSION_DENIED is the authz refusal frame (the
   * B-28 stage-2 flip): it is a RESPONSE to the request that was refused, so it also
   * rejects that request's promise — but it is surfaced globally here too so any op
   * (whichever caller sent it) shows the user a clear "no permission" message.
   */
  setDenyHandler(fn: (payload: unknown) => void): void {
    this.denyHandler = fn
  }

  // --- lifecycle -----------------------------------------------------------

  /** Open the connection eagerly (idempotent). Cancels any pending reconnect. */
  connect(): void {
    if (this.reconnectCancel) {
      this.reconnectCancel()
      this.reconnectCancel = null
    }
    if (this.connecting || (this.ws && this.ws.readyState === WS_OPEN)) return
    this.connecting = true
    this.closedByUser = false
    this.setState({ status: 'connecting', retryAt: null })

    const ws = this.factory(this.url)
    this.ws = ws
    ws.onopen = () => {
      this.connecting = false
      const t = this.now()
      this.setState({
        status: 'connected',
        attempt: 0,
        connectedSince: t,
        lastConnectedAt: t,
        retryAt: null,
      })
      const queued = this.outbox.splice(0)
      for (const frame of queued) ws.send(frame)
    }
    ws.onmessage = (ev) => this.handleMessage(ev.data)
    ws.onclose = () => {
      this.connecting = false
      this.ws = null
      this.setState({ connectedSince: null })
      this.failAll(new Error('WebSocket connection closed'))
      if (this.closedByUser) {
        this.setState({ status: 'disconnected', retryAt: null })
        return
      }
      this.scheduleReconnect()
    }
    ws.onerror = () => {
      // 'close' follows and drives reconnect; nothing to do here.
    }
  }

  /** Reset backoff and reconnect immediately (the "Reconnect now" action). */
  reconnectNow(): void {
    this.setState({ attempt: 0 })
    this.connect()
  }

  /** Permanently close (teardown / tests). Stops reconnecting. */
  close(): void {
    this.closedByUser = true
    if (this.reconnectCancel) {
      this.reconnectCancel()
      this.reconnectCancel = null
    }
    this.ws?.close()
    this.ws = null
    this.setState({ status: 'disconnected', connectedSince: null, retryAt: null })
  }

  private scheduleReconnect(): void {
    const attempt = this.state.attempt
    const base = backoffBaseMs(attempt)
    const delay = base === 0 ? 0 : Math.round(base * (0.75 + this.random() * 0.5))
    const retryAt = this.now() + delay
    // At the backoff cap we are in the persistent-failure regime: surface
    // "disconnected" (with a manual Reconnect now); below the cap, "reconnecting".
    this.setState({
      status: base >= BACKOFF_CAP_MS ? 'disconnected' : 'reconnecting',
      attempt: attempt + 1,
      retryAt,
    })
    this.reconnectCancel = this.schedule(() => {
      this.reconnectCancel = null
      this.connect()
    }, delay)
  }

  // --- request/response ----------------------------------------------------

  /**
   * Send a typed request and resolve with the full response frame ({type,
   * payload}). ERROR frames reject. Use this when the caller must branch on the
   * response type — e.g. SAVE_FILE, whose success (SAVE_ACK) and conflict
   * (CONFLICT) are distinct non-ERROR frames that would otherwise be
   * indistinguishable once the payload is unwrapped.
   */
  requestFrame(type: string, payload: unknown): Promise<WsFrame> {
    return new Promise<WsFrame>((resolve, reject) => {
      this.pending.push({ resolve, reject })
      const frame = JSON.stringify({ type, payload })
      if (this.ws && this.ws.readyState === WS_OPEN) {
        this.ws.send(frame)
      } else {
        this.outbox.push(frame)
        this.ensureConnecting()
      }
    })
  }

  /** Send a typed request and resolve with its response payload (ERROR rejects). */
  request<T>(type: string, payload: unknown): Promise<T> {
    return this.requestFrame(type, payload).then((f) => f.payload as T)
  }

  // Open a socket only if none is open/opening and no reconnect is already
  // scheduled — so a request during backoff waits for the scheduled retry
  // (preserving backoff) rather than forcing an immediate connect.
  private ensureConnecting(): void {
    if (this.ws && this.ws.readyState === WS_OPEN) return
    if (this.connecting || this.reconnectCancel) return
    this.connect()
  }

  private handleMessage(raw: unknown): void {
    if (typeof raw !== 'string') return
    let msg: { type?: string; payload?: unknown }
    try {
      msg = JSON.parse(raw) as { type?: string; payload?: unknown }
    } catch {
      return
    }
    const type = msg.type
    if (!type) return
    // NOTIFY frames are dispatched and consumed here, BEFORE the FIFO match, so
    // they never dequeue a pending request (session-2 §2 invariant).
    if (type === 'NOTIFY') {
      this.notifyHandler?.(msg.payload)
      return
    }

    const entry = this.pending.shift()
    if (!entry) {
      // A refusal with no pending request: still surface the toast.
      if (type === 'PERMISSION_DENIED') this.denyHandler?.(msg.payload)
      return
    }

    // PERMISSION_DENIED (authz refusal, B-28 stage-2): surface a global toast AND
    // reject the originating request so the caller is not left hanging.
    if (type === 'PERMISSION_DENIED') {
      this.denyHandler?.(msg.payload)
      const message =
        msg.payload && typeof msg.payload === 'object'
          ? (msg.payload as { message?: string }).message
          : undefined
      entry.reject(new Error(message ?? 'permission denied'))
      return
    }

    if (type === 'ERROR') {
      const message =
        msg.payload && typeof msg.payload === 'object'
          ? (msg.payload as { message?: string }).message
          : undefined
      entry.reject(new Error(message ?? 'Shoka error'))
      return
    }
    // Resolve with the full frame so callers can discriminate by type (a
    // CONFLICT must not be mistaken for a SAVE_ACK). `request` re-hides it.
    entry.resolve({ type, payload: msg.payload })
  }

  private failAll(err: Error): void {
    this.outbox.splice(0)
    while (this.pending.length > 0) {
      this.pending.shift()!.reject(err)
    }
  }
}

let singleton: WsRequestClient | null = null

// The WebSocket path the singleton connects to, on `window.location.host`. Default
// `/ws/ui` is Shoka's endpoint; a consumer (e.g. a product reusing these screens) can
// point the shared client at a different path via configureWsClient before first use.
let configuredPath = '/ws/ui'

/**
 * Configure the process-wide client before its first use. Currently only the socket
 * `path` is configurable (default `/ws/ui`). Calling this after the singleton has been
 * constructed has no effect on the live connection (the path is read at construction);
 * configure it at app bootstrap, before any wsClient() call. Shoka uses the default and
 * never calls this.
 */
export function configureWsClient(opts: { path?: string }): void {
  if (opts.path) configuredPath = opts.path
}

function socketUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}${configuredPath}`
}

/** The process-wide WebSocket client (path per configureWsClient), constructed on first use. */
export function wsClient(): WsRequestClient {
  if (!singleton) singleton = new WsRequestClient(socketUrl())
  return singleton
}
