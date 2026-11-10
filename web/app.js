/* ============================================================
 * 立直麻将 · 最小牌桌（S11）
 *
 * 纯 JS + DOM，无框架、无构建。职责：
 *   1. 连 WS（/api/v1/ws/games/{id}?seat=&token=），连上先发 resume(last_seq)；
 *   2. 用事件流维护牌桌（摸/打/副露/立直/宝牌/和了/流局/思考条）；
 *   3. 手牌、宝牌指示、点数、合法动作等「初始与权威状态」用
 *      GET /api/v1/games/{id} 补齐（浏览器跨源时会被 CORS 拦，见 README）；
 *   4. 断线自动重连 + resume 补齐；刷新页面用 localStorage 自动续上。
 *
 * 协议见 docs/riichi-接口契约-20261110.md §6。
 * ============================================================ */
'use strict';

(function () {
  // ── 常量 ─────────────────────────────────────────────────────

  const WIND = ['东', '南', '西', '北'];
  const HONOR = { E: '東', S: '南', W: '西', N: '北', P: '白', F: '發', C: '中' };
  const HONOR_CLS = { P: 'z-haku', F: 'z-hatsu', C: 'z-chun' };
  const HONOR_ORDER = { E: 1, S: 2, W: 3, N: 4, P: 5, F: 6, C: 7 };
  const SUIT = {
    m: { cls: 'm', label: '萬', base: 0 },
    p: { cls: 'p', label: '筒', base: 100 },
    s: { cls: 's', label: '索', base: 200 },
  };
  const PHASE_LABEL = {
    await_draw: '等待摸牌',
    await_discard: '等待打牌',
    await_response: '响应窗口',
    round_end: '本局结束',
    game_end: '对局结束',
    unknown: '—',
  };
  const PHASE_HINT = {
    await_draw: '（摸牌）',
    await_discard: '（打牌）',
    await_response: '（响应）',
  };
  /** 服务端响应窗口超时（契约 v1.3：Config.ResponseTimeout 默认 5s）。仅用于本地倒计时展示。 */
  const RESPONSE_TIMEOUT_MS = 5000;
  const LS_KEY = 'riichi-s11-demo';
  const LOG_LIMIT = 400;

  // ── 小工具 ───────────────────────────────────────────────────

  const $ = (id) => document.getElementById(id);

  /** 创建元素：el('div', 'cls', '文本')。 */
  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined && text !== null) node.textContent = String(text);
    return node;
  }

  function num(value, fallback) {
    const n = Number(value);
    return Number.isFinite(n) ? n : fallback;
  }

  function pad2(value) {
    return String(value).padStart(2, '0');
  }

  function nowHMS() {
    const d = new Date();
    return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }

  function uuid() {
    if (window.crypto && typeof window.crypto.randomUUID === 'function') {
      return window.crypto.randomUUID();
    }
    return 'a' + Math.random().toString(16).slice(2) + Date.now().toString(16);
  }

  // ── 牌面解析 / 渲染 ──────────────────────────────────────────

  /** 把线上牌面（"2m" / "r5s" / "0p" / "E" ...）解析成渲染信息。 */
  function parseTile(text) {
    const raw = String(text === undefined || text === null ? '' : text).trim();
    if (!raw) return { cls: 'unknown', label: '?', order: 900 };
    if (HONOR[raw]) {
      return {
        cls: 'z ' + (HONOR_CLS[raw] || ''),
        honor: HONOR[raw],
        label: raw,
        order: 300 + HONOR_ORDER[raw],
      };
    }
    let aka = false;
    let body = raw;
    if (/^r/i.test(body)) {
      aka = true;
      body = body.slice(1);
    } else if (/^0/.test(body)) {
      aka = true;
      body = '5' + body.slice(1);
    }
    const m = /^([1-9])([mps])$/i.exec(body);
    if (!m) return { cls: 'unknown', label: raw, order: 900 };
    const suit = SUIT[m[2].toLowerCase()];
    const value = Number(m[1]);
    return {
      cls: suit.cls,
      num: value,
      suitLabel: suit.label,
      aka: aka,
      label: raw,
      order: suit.base + value,
    };
  }

  function tileOrder(text) {
    return parseTile(text).order;
  }

  /** 渲染一张牌。【text】牌面；opts: {size, clickable, title} */
  function tileEl(text, opts) {
    const options = opts || {};
    const info = parseTile(text);
    const node = el('span', 'tile ' + info.cls + ' ' + (options.size || 'sm'));
    if (info.aka) node.classList.add('aka');
    if (info.honor) {
      node.appendChild(el('b', 'h', info.honor));
    } else if (info.num) {
      node.appendChild(el('b', 'n', String(info.num)));
      node.appendChild(el('i', 's', info.suitLabel));
    } else {
      node.appendChild(el('b', 'n', info.label));
    }
    node.title = options.title || String(text);
    if (options.clickable) {
      node.classList.add('clickable');
      node.tabIndex = 0;
      node.setAttribute('role', 'button');
    }
    return node;
  }

  // ── 全局状态 ─────────────────────────────────────────────────

  const S = {
    // 连接参数
    serverUrl: 'http://127.0.0.1:8099',
    gameId: '',
    seat: 0,
    token: '',

    // 连接状态
    ws: null,
    wsReady: false,
    userClosed: false,
    connecting: false,
    reconnectAttempt: 0,
    reconnectTimer: null,
    reconnectAt: 0,
    pingTimer: null,
    rtt: null,

    // 状态来源：same-origin（serve.py 反代）| direct（后端 CORS）| events-only（仅事件流）
    source: 'none',
    apiBase: '',
    historyLoaded: false,
    loggedMaxSeq: 0,

    // 对局状态（与 game.Observation / 事件流对齐）
    seq: 0,
    lastEventSeq: 0,
    phase: '',
    turn: -1,
    wallRemaining: 0,
    hand: [],
    drawn: null,
    handKnown: false,
    melds: [[], [], [], []],
    discards: [[], [], [], []],
    dora: [],
    scores: [25000, 25000, 25000, 25000],
    riichi: [false, false, false, false],
    roundWind: 0,
    kyoku: 1,
    honba: 0,
    riichiSticks: 0,
    dealer: 0,
    dealerKnown: false,
    legal: [],
    lastDiscard: null,

    // 响应窗口（契约 v1.3：{type:"responses", seq, seat, options[]}）
    responses: null,
    responseDeadline: 0,
    responseTimer: null,
    responseLeft: 0,

    // 交互
    pending: false,
    pendingTimer: null,
    thoughtTimer: null,
    logPaused: false,
    logCount: 0,
    logMaxSeq: 0,
    syncTimer: null,
    lastError: '',
  };

  // ── DOM ──────────────────────────────────────────────────────

  const dom = {};
  function cacheDom() {
    dom.form = $('conn-form');
    dom.serverUrl = $('server-url');
    dom.gameId = $('game-id');
    dom.seat = $('seat');
    dom.token = $('token');
    dom.btnTokenToggle = $('btn-token-toggle');
    dom.btnNew = $('btn-new');
    dom.btnConnect = $('btn-connect');
    dom.btnDisconnect = $('btn-disconnect');
    dom.statusPill = $('status-pill');
    dom.seqPill = $('seq-pill');
    dom.sourcePill = $('source-pill');
    dom.rttPill = $('rtt-pill');
    dom.banner = $('banner');
    dom.table = $('table');
    dom.zoneTop = document.querySelector('[data-zone="top"]');
    dom.zoneLeft = document.querySelector('[data-zone="left"]');
    dom.zoneRight = document.querySelector('[data-zone="right"]');
    dom.zoneBottom = document.querySelector('[data-zone="bottom"]');
    dom.center = $('center');
    dom.thoughtBar = $('thought-bar');
    dom.thoughtAgent = $('thought-agent');
    dom.thoughtText = $('thought-text');
    dom.actions = $('actions');
    dom.actionHint = $('action-hint');
    dom.btnDrop = $('btn-drop');
    dom.btnResync = $('btn-resync');
    dom.eventLog = $('event-log');
    dom.eventCount = $('event-count');
    dom.logPause = $('log-pause');
    dom.btnClearLog = $('btn-clear-log');
  }

  // ── 横幅 / 提示 / 状态胶囊 ───────────────────────────────────

  function setBanner(text, kind) {
    if (!text) {
      dom.banner.className = 'banner hidden';
      dom.banner.textContent = '';
      return;
    }
    dom.banner.className = 'banner ' + (kind || 'info');
    dom.banner.textContent = text;
  }

  function setStatus(text, kind) {
    dom.statusPill.textContent = text;
    dom.statusPill.className = 'pill ' + (kind || 'idle');
  }

  function updatePills() {
    dom.seqPill.textContent = 'seq ' + (S.lastEventSeq || '—');
    const label =
      S.source === 'same-origin'
        ? '状态源 同源代理'
        : S.source === 'direct'
          ? '状态源 直连'
          : S.source === 'events-only'
            ? '状态源 仅事件流'
            : '状态源 —';
    dom.sourcePill.textContent = label;
    dom.sourcePill.className =
      'pill ' + (S.source === 'events-only' ? 'warn' : S.source === 'none' ? 'idle' : 'ok');
    dom.rttPill.textContent = 'RTT ' + (S.rtt === null ? '—' : S.rtt + 'ms');
    dom.btnDisconnect.disabled = !S.wsReady && !S.connecting;
    dom.btnConnect.textContent = S.wsReady ? '重连' : '连接';
    document.title =
      (S.gameId ? '[' + S.gameId.slice(0, 8) + '] ' : '') + '立直麻将 · 最小牌桌（S11）';
  }

  // ── 状态来源解析 ─────────────────────────────────────────────

  function sameOriginPossible() {
    return location.protocol === 'http:' || location.protocol === 'https:';
  }

  function serverBase() {
    return (S.serverUrl || '').replace(/\/+$/, '');
  }

  /** 把 ws(s):// 归一化成 http(s)://，方便用户直接粘 ws 地址。 */
  function httpBaseFromServer() {
    const base = serverBase();
    if (/^ws:\/\//i.test(base)) return base.replace(/^ws/i, 'http');
    if (/^wss:\/\//i.test(base)) return base.replace(/^wss/i, 'https');
    return base;
  }

  function apiCandidates() {
    // 顺序很重要：先直连（后端带 -cors 时的主路径，file:// 打开也走这条），
    // 失败再退回同源反代（serve.py / nginx，后端没开 CORS 时的兜底）。
    const list = [];
    const direct = httpBaseFromServer();
    if (/^https?:\/\//i.test(direct)) list.push(direct);
    if (sameOriginPossible()) list.push('');
    return list;
  }

  function wsUrl() {
    const base = serverBase();
    let root;
    if (/^wss?:\/\//i.test(base)) root = base;
    else if (/^https?:\/\//i.test(base)) root = base.replace(/^http/i, 'ws');
    else if (sameOriginPossible()) root = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host;
    else root = 'ws://127.0.0.1:8099';
    return (
      root +
      '/api/v1/ws/games/' +
      encodeURIComponent(S.gameId) +
      '?seat=' +
      encodeURIComponent(String(S.seat)) +
      '&token=' +
      encodeURIComponent(S.token)
    );
  }

  function httpError(status, body) {
    const err = new Error('HTTP ' + status + (body ? '：' + body.slice(0, 160) : ''));
    err.httpStatus = status;
    return err;
  }

  /** 调 HTTP API：先试同源反代，再试直连；返回 {data, base}。 */
  async function apiJson(path, options) {
    const opts = options || {};
    const headers = Object.assign({}, opts.headers || {});
    if (S.token) headers.Authorization = 'Bearer ' + S.token;
    const candidates = apiCandidates();
    let lastErr = null;
    for (let i = 0; i < candidates.length; i += 1) {
      const base = candidates[i];
      try {
        const resp = await fetch(base + path, Object.assign({}, opts, { headers: headers }));
        if (!resp.ok) {
          if (base === '' && resp.status === 404) {
            lastErr = new Error('同源代理未命中（404），尝试直连');
            continue;
          }
          let body = '';
          try {
            body = await resp.text();
          } catch (ignored) {
            body = '';
          }
          throw httpError(resp.status, body);
        }
        const data = await resp.json();
        return { data: data, base: base };
      } catch (err) {
        if (err && err.httpStatus) throw err; // 服务端明确拒绝（401/404/400…）→ 不再换源
        lastErr = err;
      }
    }
    throw lastErr || new Error('无法连接 HTTP API（网络或浏览器跨源限制）');
  }

  // ── 状态应用 ─────────────────────────────────────────────────

  function normalizeMelds(value) {
    const out = [[], [], [], []];
    for (let seat = 0; seat < 4; seat += 1) {
      const list = value && Array.isArray(value[seat]) ? value[seat] : [];
      out[seat] = list.map(function (m) {
        return {
          kind: m && m.kind ? String(m.kind) : 'meld',
          tiles: m && Array.isArray(m.tiles) ? m.tiles.slice() : [],
          from: m && Number.isFinite(m.from) ? m.from : -1,
        };
      });
    }
    return out;
  }

  function normalizeDiscards(value) {
    const out = [[], [], [], []];
    for (let seat = 0; seat < 4; seat += 1) {
      const list = value && Array.isArray(value[seat]) ? value[seat] : [];
      out[seat] = list.slice();
    }
    return out;
  }

  /** 把 GET /api/v1/games/{id} 的 Observation 应用到 UI 状态。 */
  function applyObservation(data) {
    S.seq = num(data.seq, 0);
    S.phase = data.phase || '';
    S.turn = num(data.turn, -1);
    S.wallRemaining = num(data.wall_remaining, 0);
    S.hand = Array.isArray(data.hand) ? data.hand.slice() : [];
    S.drawn = data.drawn || null;
    S.melds = normalizeMelds(data.melds);
    S.discards = normalizeDiscards(data.discards);
    S.dora = Array.isArray(data.dora_indicators) ? data.dora_indicators.slice() : [];
    S.scores = Array.isArray(data.scores) ? data.scores.slice() : S.scores;
    S.riichi = Array.isArray(data.riichi) ? data.riichi.slice() : S.riichi;
    S.roundWind = num(data.round_wind, 0);
    S.kyoku = num(data.kyoku, 1);
    S.honba = num(data.honba, 0);
    S.riichiSticks = num(data.riichi_sticks, 0);
    S.legal = Array.isArray(data.legal_actions) ? data.legal_actions : [];
    S.handKnown = true;
    S.lastEventSeq = Math.max(S.lastEventSeq, S.seq);
    // 权威状态说「已经不在响应阶段」→ 收起残留的响应按钮
    // （S6 的超时路径不会发 response_window_end，只能靠状态同步兜底）
    if (S.responses && S.phase !== 'await_response') {
      clearResponseWindow(false);
    }
  }

  /** 从回放事件里补「状态接口没有的」信息（庄家）+ 把历史写进日志。 */
  async function loadReplay() {
    let data;
    try {
      const result = await apiJson('/api/v1/games/' + encodeURIComponent(S.gameId) + '/replay');
      data = result.data;
    } catch (err) {
      return; // 回放失败不影响主流程
    }
    const events = Array.isArray(data.events) ? data.events : [];
    for (let i = 0; i < events.length; i += 1) {
      const ev = events[i];
      if (ev.kind === 'round_start' && ev.payload) {
        S.dealer = num(ev.payload.dealer, S.dealer);
        S.dealerKnown = true;
      }
      if (!S.historyLoaded && ev.seq <= S.seq) {
        appendLog(ev, true);
        S.loggedMaxSeq = Math.max(S.loggedMaxSeq, num(ev.seq, 0));
      }
    }
    S.historyLoaded = true;
    render();
  }

  // ── 连接 ─────────────────────────────────────────────────────

  function readForm() {
    S.serverUrl = (dom.serverUrl.value || '').trim() || 'http://127.0.0.1:8099';
    S.gameId = (dom.gameId.value || '').trim();
    S.seat = num(dom.seat.value, 0);
    S.token = (dom.token.value || '').trim();
    saveSettings();
  }

  function saveSettings() {
    try {
      localStorage.setItem(
        LS_KEY,
        JSON.stringify({
          serverUrl: S.serverUrl,
          gameId: S.gameId,
          seat: S.seat,
          token: S.token,
        })
      );
    } catch (err) {
      /* 隐私模式下 localStorage 可能不可用，忽略 */
    }
  }

  function restoreSettings() {
    try {
      const raw = localStorage.getItem(LS_KEY);
      if (!raw) return false;
      const saved = JSON.parse(raw);
      if (saved.serverUrl) S.serverUrl = saved.serverUrl;
      if (saved.gameId) S.gameId = saved.gameId;
      if (Number.isFinite(saved.seat)) S.seat = saved.seat;
      if (saved.token) S.token = saved.token;
      dom.serverUrl.value = S.serverUrl;
      dom.gameId.value = S.gameId;
      dom.seat.value = String(S.seat);
      dom.token.value = S.token;
      return !!(S.gameId && S.token);
    } catch (err) {
      return false;
    }
  }

  function clearReconnect() {
    if (S.reconnectTimer) {
      clearTimeout(S.reconnectTimer);
      S.reconnectTimer = null;
    }
  }

  function scheduleReconnect(reason) {
    if (S.userClosed) return;
    clearReconnect();
    const attempt = S.reconnectAttempt + 1;
    S.reconnectAttempt = attempt;
    if (attempt > 8) {
      setStatus('已停止自动重连', 'err');
      setBanner(
        '自动重连 8 次仍失败（' + reason + '）。请检查：服务是否在跑、令牌是否过期（默认 2h）、座位是否与令牌一致。修好后点「连接」。',
        'err'
      );
      return;
    }
    const delay = Math.min(1200 * Math.pow(1.8, attempt - 1), 8000);
    S.reconnectAt = Date.now() + delay;
    setStatus('断开，' + Math.round(delay / 1000) + 's 后重连…', 'warn');
    logEvent({ kind: 'reconnect', seat: -1, extra: reason + '，' + delay + 'ms 后重连（第 ' + attempt + ' 次）' });
    S.reconnectTimer = setTimeout(function () {
      S.reconnectTimer = null;
      connect({ isReconnect: true });
    }, delay);
  }

  function closeSocket(silent) {
    stopPing();
    if (S.ws) {
      const ws = S.ws;
      S.ws = null;
      S.wsReady = false;
      try {
        ws.onopen = null;
        ws.onmessage = null;
        ws.onerror = null;
        ws.onclose = null;
        ws.close();
      } catch (err) {
        /* 忽略关闭异常 */
      }
    }
    S.wsReady = false;
    if (!silent) updatePills();
  }

  /** 连接入口：先拉权威状态（首次），再开 WS 并 resume。 */
  async function connect(opts) {
    const options = opts || {};
    readForm();
    if (!S.gameId) {
      setBanner('请先填写「对局 ID」，或点「新建对局」由页面建一局。', 'warn');
      return;
    }
    if (!S.token) {
      setBanner('请填写 JWT 令牌。签发命令见 web/README.md（-issue-token 0 拿座位 0 的令牌）。', 'warn');
      return;
    }
    if (S.connecting) return;
    S.connecting = true;
    S.userClosed = false;
    clearReconnect();
    setBanner('');
    S.rtt = null;
    if (S.gameId !== S._logGameId) {
      resetLog(); // 换了对局：日志从头开始
      S._logGameId = S.gameId;
    }
    updatePills();

    const sameGame = S.handKnown && S.gameId === S._connectedGameId;
    if (!options.isReconnect || !sameGame) {
      setStatus('同步状态…', 'busy');
      try {
        const result = await apiJson('/api/v1/games/' + encodeURIComponent(S.gameId));
        applyObservation(result.data);
        S.apiBase = result.base;
        S.source = result.base === '' ? 'same-origin' : 'direct';
        setBanner('');
        await loadReplay();
      } catch (err) {
        S.source = 'events-only';
        setStatus('WS 事件流模式', 'warn');
        setBanner(
          '⚠ 拿不到对局状态（' +
            (err && err.message ? err.message : err) +
            '）。浏览器跨源策略会拦住 HTTP 请求时，页面只能靠 WS 事件流：' +
            '牌河/剩余牌数/点数可显示，但看不到自己的手牌、也无法点牌打牌。' +
            '解决办法：用 `python web/serve.py` 打开页面（同源反代），或让后端加 CORS 头。',
          'warn'
        );
      }
    }

    openSocket(sameGame ? S.lastEventSeq : Math.max(S.lastEventSeq, S.seq));
    S.connecting = false;
  }

  function openSocket(resumeFrom) {
    closeSocket(true);
    const url = wsUrl();
    logEvent({
      kind: 'ws_connect',
      seat: -1,
      extra: url.replace(/token=[^&]+/, 'token=***') + ' · resume(' + resumeFrom + ')',
    });
    let ws;
    try {
      ws = new WebSocket(url);
    } catch (err) {
      setStatus('WS 地址非法', 'err');
      return;
    }
    S.ws = ws;
    S._connectedGameId = S.gameId;
    setStatus('连接中…', 'busy');

    ws.onopen = function () {
      if (S.ws !== ws) return;
      S.wsReady = true;
      S.reconnectAttempt = 0;
      setStatus('已连接（' + S.sourceLabel() + '）', 'ok');
      updatePills();
      send({ type: 'resume', last_seq: resumeFrom });
      startPing();
      if (S.source === 'events-only') {
        // 事件流模式：resume(0) 时服务端会回放全部历史；后续只显示事件
        setBanner(
          '⚠ 仅事件流模式：看不到自己的手牌（初始配牌不在事件里）。用 `python web/serve.py` 打开，或让后端加 CORS。',
          'warn'
        );
      }
    };

    ws.onmessage = function (event) {
      if (S.ws !== ws) return;
      let msg;
      try {
        msg = JSON.parse(event.data);
      } catch (err) {
        console.warn('WS 消息不是 JSON', event.data);
        return;
      }
      handleMessage(msg);
    };

    ws.onerror = function () {
      /* onclose 会紧跟着触发，这里不重复处理 */
    };

    ws.onclose = function (event) {
      if (S.ws !== ws) return;
      S.ws = null;
      S.wsReady = false;
      stopPing();
      updatePills();
      if (S.userClosed) {
        setStatus('已断开', 'idle');
        return;
      }
      scheduleReconnect('WS 关闭 code=' + event.code);
    };
  }

  // 给状态栏用的一句话来源说明
  S.sourceLabel = function () {
    if (S.source === 'same-origin') return '同源代理';
    if (S.source === 'direct') return '直连';
    return '事件流';
  };

  function send(obj) {
    if (!S.ws || S.ws.readyState !== WebSocket.OPEN) {
      toast('WebSocket 未连接，动作未发送');
      return false;
    }
    try {
      S.ws.send(JSON.stringify(obj));
      return true;
    } catch (err) {
      toast('发送失败：' + err.message);
      return false;
    }
  }

  function startPing() {
    stopPing();
    S.pingTimer = setInterval(function () {
      if (S.ws && S.ws.readyState === WebSocket.OPEN) {
        send({ type: 'ping', ts: Date.now() });
      }
    }, 20000);
  }

  function stopPing() {
    if (S.pingTimer) {
      clearInterval(S.pingTimer);
      S.pingTimer = null;
    }
  }

  function disconnect() {
    S.userClosed = true;
    clearReconnect();
    closeSocket();
    setStatus('已断开', 'idle');
    updatePills();
    logEvent({ kind: 'ws_close', seat: -1, extra: '用户主动断开' });
  }

  /** 模拟网络中断：只关 WS、不置 userClosed，交给自动重连 + resume 补齐。 */
  function simulateDrop() {
    S.userClosed = false;
    if (!S.ws) {
      toast('当前没有 WebSocket 可断（请先连接）');
      return;
    }
    logEvent({ kind: 'ws_close', seat: -1, extra: '模拟断线（本地关闭 WS，等待自动重连）' });
    try {
      S.ws.close();
    } catch (err) {
      /* 关闭失败也交给 onclose/定时器兜底 */
    }
  }

  // ── 消息分发 ─────────────────────────────────────────────────

  function handleMessage(msg) {
    switch (msg.type) {
      case 'event':
        onEvent(msg);
        break;
      case 'resumed':
        onResumed(msg);
        break;
      case 'pong':
        S.rtt = Math.max(0, Date.now() - num(msg.ts, Date.now()));
        updatePills();
        break;
      case 'responses':
        onResponses(msg);
        break;
      case 'response_window_end':
        onResponseWindowEnd(msg);
        break;
      case 'agent_thought':
        onAgentThought(msg);
        break;
      case 'error':
        onServerError(msg);
        break;
      default:
        console.warn('未知消息类型', msg);
        break;
    }
  }

  function onEvent(msg) {
    appendLog(msg, false);
    const seq = num(msg.seq, 0);
    if (seq > 0 && seq <= S.lastEventSeq) {
      return; // resume 重放的旧事件：日志留痕，状态不重复应用
    }
    S.lastEventSeq = Math.max(S.lastEventSeq, seq);
    S.seq = Math.max(S.seq, seq);
    // 响应窗口只会由「响应动作 / 超时 / 结算」结束；窗口之后还能收到新事件
    // 说明窗口已经结算（超时路径服务端不发 response_window_end）
    if (S.responses && seq > S.responses.seq) {
      clearResponseWindow(false);
    }
    applyEvent(msg);
    if (S.pending) {
      S.pending = false;
      if (S.pendingTimer) clearTimeout(S.pendingTimer);
    }
    render();
  }

  function onResumed(msg) {
    const from = num(msg.from_seq, 0);
    const to = num(msg.to_seq, 0);
    // seq 传 0：resumed 不是对局事件，不能参与「按 seq 去重」（否则会被刚补发的事件吞掉）
    appendLog({ kind: 'resumed', seat: -1, seq: 0, payload: { from_seq: from, to_seq: to } }, false);
    const before = S.lastEventSeq;
    S.lastEventSeq = Math.max(S.lastEventSeq, to);
    S.seq = Math.max(S.seq, to);
    // 服务端声明「到 to 为止都补给你了」；若本地还落后，主动拉一次权威状态兜底
    if (to > before && S.source !== 'events-only' && S.handKnown) {
      // 事件通常已经逐条到达并应用；这里只在明显落后时兜底
      if (to - before > 50) scheduleSync(0);
    }
    updatePills();
    render();
  }

  function onServerError(msg) {
    const code = msg.code || 'ERROR';
    S.lastError = msg.message || '';
    appendLog({ kind: 'error', seat: -1, payload: { code: code, message: msg.message } }, false);
    setBanner('服务端错误 ' + code + '：' + (msg.message || ''), 'err');
    S.pending = false;
    if (S.pendingTimer) clearTimeout(S.pendingTimer);
    if (code === 'STALE_SEQ') {
      scheduleSync(0);
    }
    if (code === 'SEAT_MISMATCH') {
      setBanner('令牌与「座位」不一致：请用 `-issue-token ' + S.seat + '` 重新签发令牌。', 'err');
    }
    renderActions();
  }

  // ── 响应窗口（契约 v1.3，task-14）────────────────────────────
  //
  // 服务端在「你可以对别人打出的牌做响应」时按座位推：
  //   {"type":"responses","seq":43,"seat":1,"options":[{"kind":"ron","tile":"5m"}, ...]}
  // 必须用 {"type":"action","seq":<同一个 seq>, "action":{...}} 回传；
  // 窗口结束广播 {"type":"response_window_end","seq":44}；5s 不选服务端按 pass 结算。

  /** 收到 responses：把 options 渲染成按钮，起 5s 本地倒计时。 */
  function onResponses(msg) {
    const seat = num(msg.seat, S.seat);
    const options = Array.isArray(msg.options) ? msg.options : [];
    if (!options.length) return;
    if (seat !== S.seat) {
      // 服务端只推给本人；防御：别把别人的窗口当成自己的
      logEvent({ kind: 'responses', seat: seat, extra: '（他家的响应窗口，忽略）' });
      return;
    }
    clearResponseWindow(true);
    S.responses = { seq: num(msg.seq, S.lastEventSeq), seat: seat, options: options };
    S.phase = 'await_response';
    S.turn = seat;
    S.pending = false;
    if (S.pendingTimer) clearTimeout(S.pendingTimer);
    S.responseDeadline = Date.now() + RESPONSE_TIMEOUT_MS;
    S.responseLeft = Math.ceil(RESPONSE_TIMEOUT_MS / 1000);
    S.responseTimer = setInterval(tickResponseTimer, 200);
    logEvent({ kind: 'responses', seat: seat, payload: { options: options } });
    setBanner(
      '响应窗口：' +
        options.map(responseOptionLabel).join(' / ') +
        '（' +
        Math.round(RESPONSE_TIMEOUT_MS / 1000) +
        's 内不选按 pass 结算）',
      'info'
    );
    render();
  }

  /** 本地倒计时（只重绘动作区，不动牌桌）。 */
  function tickResponseTimer() {
    if (!S.responses) {
      stopResponseTimer();
      return;
    }
    S.responseLeft = Math.max(0, Math.ceil((S.responseDeadline - Date.now()) / 1000));
    if (S.responseLeft <= 0) {
      stopResponseTimer();
      logEvent({ kind: 'responses', seat: S.seat, extra: '本地倒计时归零，等待服务端按 pass 结算' });
      scheduleSync(0); // 拉权威状态：窗口结算后 phase 会离开 await_response
      setTimeout(recheckResponseWindowAfterTimeout, 1500);
    }
    renderActions();
  }

  /** 超时兜底：正常情况 response_window_end / 状态同步已经清掉窗口；否则本地收起，避免永久卡死。 */
  function recheckResponseWindowAfterTimeout() {
    if (!S.responses) return;
    if (S.phase !== 'await_response') {
      clearResponseWindow(false);
      render();
      return;
    }
    logEvent({
      kind: 'responses',
      seat: S.seat,
      extra: '超时后仍未收到 response_window_end 且状态仍是 await_response，本地收起按钮',
    });
    clearResponseWindow(false);
    scheduleSync(0);
    render();
  }

  function stopResponseTimer() {
    if (S.responseTimer) {
      clearInterval(S.responseTimer);
      S.responseTimer = null;
    }
  }

  function clearResponseWindow(keepBanner) {
    stopResponseTimer();
    S.responses = null;
    S.responseLeft = 0;
    // 响应窗口的提示横幅要收掉；用文本前缀判断，避免 className 带多个类时漏清
    if (!keepBanner && dom.banner.textContent && dom.banner.textContent.indexOf('响应窗口') === 0) {
      setBanner('');
    }
  }

  /** 收到 response_window_end：收起按钮，并同步一次权威状态（副露/和了/轮转都可能变了）。 */
  function onResponseWindowEnd(msg) {
    clearResponseWindow(false);
    S.pending = false;
    if (S.pendingTimer) clearTimeout(S.pendingTimer);
    logEvent({ kind: 'response_window_end', seat: -1, payload: { seq: num(msg.seq, 0) } });
    scheduleSync(120);
    render();
  }

  /** 响应按钮文案；服务端 chi/pon/kan 的 action 不带 tile，用 consumed/最后一张牌河补。 */
  function responseOptionLabel(option) {
    if (!option) return '—';
    const consumed = Array.isArray(option.consumed) ? option.consumed : [];
    const lastDiscard = S.lastDiscard && S.lastDiscard.tile ? S.lastDiscard.tile : '';
    const tile = option.tile || lastDiscard || consumed[0] || '';
    switch (option.kind) {
      case 'ron':
        return '荣和 ' + tile;
      case 'pon':
        return '碰 ' + tile;
      case 'chi': {
        const pair = consumed.join('');
        return '吃 ' + tile + (pair ? '（' + pair + '）' : '');
      }
      case 'kan_daiminkan':
        return '大明杠 ' + tile;
      case 'kan_shouminkan':
        return '加杠 ' + tile;
      case 'kan_ankan':
        return '暗杠 ' + tile;
      case 'pass':
        return 'pass（跳过）';
      default:
        return actionLabel(option);
    }
  }

  function responseOptionTitle(option) {
    try {
      return '提交 ' + JSON.stringify(option);
    } catch (err) {
      return '提交响应动作';
    }
  }

  /** 提交响应动作：seq 必须回填 responses.seq（服务端用它校验动作是否过期）。 */
  function submitResponse(option) {
    if (!S.responses || S.pending) return;
    if (Date.now() > S.responseDeadline) {
      toast('响应已超时，服务端会按 pass 结算');
      return;
    }
    const action = { kind: option.kind };
    if (option.tile) action.tile = option.tile;
    if (Array.isArray(option.consumed) && option.consumed.length) action.consumed = option.consumed;
    const seq = S.responses.seq;
    clearResponseWindow(false);
    submitAction(action, { seq: seq, label: '响应：' + responseOptionLabel(option) });
  }

  /** AI 决策广播（顶层消息，不是 event）：渲染思考条 + 写日志。 */
  function onAgentThought(msg) {
    const payload = {
      agent: msg.agent,
      elapsed_ms: msg.elapsed_ms,
      chosen: msg.chosen,
      candidates: msg.candidates,
    };
    showThought(payload, num(msg.seat, -1));
    appendLog({ kind: 'agent_thought', seat: num(msg.seat, -1), payload: payload }, false);
  }

  // ── 事件 → 状态 ─────────────────────────────────────────────

  /** 未知/复杂事件后，拉一次权威状态（防 UI 与真状态漂移）。 */
  function scheduleSync(delay) {
    if (S.syncTimer) return;
    S.syncTimer = setTimeout(async function () {
      S.syncTimer = null;
      if (!S.gameId) return;
      try {
        const result = await apiJson('/api/v1/games/' + encodeURIComponent(S.gameId));
        applyObservation(result.data);
        S.apiBase = result.base;
        S.source = result.base === '' ? 'same-origin' : 'direct';
        if (S.source !== 'events-only' && dom.banner.className.indexOf('banner err') === 0) setBanner('');
        render();
      } catch (err) {
        if (S.source !== 'events-only') {
          S.source = 'events-only';
          setBanner('状态同步失败，已降级为仅事件流：' + (err && err.message ? err.message : err), 'warn');
        }
      }
    }, typeof delay === 'number' ? delay : 200);
  }

  function removeOne(list, tile) {
    for (let i = list.length - 1; i >= 0; i -= 1) {
      if (list[i] === tile) {
        list.splice(i, 1);
        return true;
      }
    }
    return false;
  }

  function applyRoundMeta(payload) {
    const p = payload || {};
    S.roundWind = num(p.round_wind, S.roundWind);
    S.kyoku = num(p.kyoku, S.kyoku);
    S.honba = num(p.honba, S.honba);
    if (p.dealer !== undefined) {
      S.dealer = num(p.dealer, S.dealer);
      S.dealerKnown = true;
    }
    if (Array.isArray(p.scores)) S.scores = p.scores.slice();
  }

  function applyEvent(msg) {
    const p = msg.payload || {};
    switch (msg.kind) {
      case 'round_start':
        applyRoundMeta(p);
        S.discards = [[], [], [], []];
        S.melds = [[], [], [], []];
        S.hand = [];
        S.drawn = null;
        S.handKnown = false;
        S.riichi = [false, false, false, false];
        S.dora = [];
        S.legal = [];
        S.phase = 'await_draw';
        S.turn = S.dealer;
        S.lastDiscard = null;
        S.historyLoaded = false;
        S.loggedMaxSeq = 0;
        S.syncTimer = null;
        setBanner('第 ' + WIND[S.roundWind] + ' ' + S.kyoku + ' 局开始（本场 ' + S.honba + '）', 'info');
        scheduleSync(150); // 新手牌只能从 HTTP 状态接口拿
        break;

      case 'draw':
        S.wallRemaining = num(p.wall_remaining, S.wallRemaining);
        S.turn = msg.seat;
        S.phase = 'await_discard';
        if (msg.seat === S.seat) {
          const tile = p.tile;
          if (tile) {
            S.hand.push(tile);
            S.drawn = tile;
            S.handKnown = true;
          }
        }
        break;

      case 'discard':
        if (p.tile) {
          S.lastDiscard = { seat: msg.seat, tile: p.tile };
          S.discards[msg.seat].push(p.tile);
          if (msg.seat === S.seat) {
            removeOne(S.hand, p.tile);
            S.drawn = null;
          }
        }
        if (p.riichi_declared) S.riichi[msg.seat] = true;
        S.turn = (msg.seat + 1) % 4;
        S.phase = 'await_draw';
        break;

      case 'chi':
      case 'pon':
      case 'kan': {
        const meld = buildMeldFromEvent(msg);
        if (meld) {
          S.melds[msg.seat].push(meld);
          // 我方副露：把消耗掉的手牌从手里扣掉
          if (msg.seat === S.seat) {
            const consumed = Array.isArray(p.consumed) ? p.consumed : [];
            consumed.forEach(function (tile) {
              removeOne(S.hand, tile);
            });
            if (p.tile && meld.from !== S.seat) removeOne(S.hand, p.tile);
          }
          // 被鸣的那张牌会从放铳者牌河里拿走（权威状态由同步兜底）
          if (Number.isFinite(p.from) && Array.isArray(S.discards[p.from]) && p.tile) {
            const river = S.discards[p.from];
            if (river.length && river[river.length - 1] === p.tile) river.pop();
          }
          S.turn = msg.seat;
          S.phase = 'await_discard';
        } else {
          scheduleSync(120);
        }
        scheduleSync(200);
        break;
      }

      case 'riichi':
        S.riichi[msg.seat] = true;
        break;

      case 'dora_reveal':
        if (p.indicator) S.dora.push(p.indicator);
        else if (Array.isArray(p.dora) && p.dora.length) S.dora = p.dora.slice();
        break;

      case 'tsumo':
      case 'ron': {
        const label = msg.kind === 'tsumo' ? '自摸' : '荣和';
        const extra = p.han !== undefined ? '（' + p.han + ' 翻 ' + (p.fu === undefined ? '?' : p.fu) + ' 符）' : '';
        setBanner(
          '🀄 ' + seatShort(msg.seat) + ' ' + label + ' ' + (p.winning_tile || '') + extra + '，等待下一局…',
          'info'
        );
        S.legal = [];
        scheduleSync(250);
        break;
      }

      case 'ryuukyoku':
        setBanner('流局（' + (p.reason || '荒牌') + '）', 'info');
        S.legal = [];
        scheduleSync(250);
        break;

      case 'round_end':
        setBanner('本局结束' + (p.honba !== undefined ? '，下一局本场 ' + p.honba : ''), 'info');
        S.legal = [];
        scheduleSync(250);
        break;

      case 'game_end':
        setBanner('🏁 对局结束：' + fmtScores(p.final_scores || S.scores), 'info');
        S.legal = [];
        break;

      case 'error':
        setBanner('对局内错误 ' + (p.code || '') + '：' + (p.message || ''), 'err');
        break;

      default:
        // M2 的新事件（kawa 等）：不猜语义，直接同步权威状态
        scheduleSync(200);
        break;
    }
  }

  function buildMeldFromEvent(msg) {
    const p = msg.payload || {};
    // task-14 起服务端 payload 带 tiles（完整 3/4 张）、consumed、from、meld_kind
    const tiles = Array.isArray(p.tiles) ? p.tiles.slice() : [];
    if (!tiles.length) {
      if (p.tile) tiles.push(p.tile);
      const consumed = Array.isArray(p.consumed) ? p.consumed : [];
      consumed.forEach(function (tile) {
        tiles.push(tile);
      });
    }
    if (!tiles.length) return null;
    return {
      kind: p.meld_kind || p.kan_type || msg.kind,
      tiles: tiles,
      from: Number.isFinite(p.from) ? p.from : -1,
    };
  }

  // ── 动作提交 ─────────────────────────────────────────────────

  function canDiscardNow() {
    if (!S.handKnown || S.pending) return false;
    if (S.phase !== 'await_discard' || S.turn !== S.seat) return false;
    if (S.legal.length > 0) {
      return S.legal.some(function (a) {
        return a && a.kind === 'discard';
      });
    }
    return true; // 事件流模式：由服务端做最终校验
  }

  function submitAction(action, opts) {
    const options = opts || {};
    if (S.pending) return;
    if (!S.wsReady) {
      toast('WebSocket 未连接');
      return;
    }
    const msg = { type: 'action', action_id: uuid(), action: action };
    // 响应窗口要求回填 responses.seq（opts.seq）；普通动作回填最近事件 seq
    const seq = options.seq !== undefined ? options.seq : S.lastEventSeq;
    if (seq > 0) msg.seq = seq;
    S.pending = true;
    S.pendingTimer = setTimeout(function () {
      if (S.pending) {
        S.pending = false;
        toast('服务端 4s 未回事件，已解除操作锁（可点「重新同步」）');
        renderActions();
      }
    }, 4000);
    if (send(msg)) {
      logEvent({
        kind: 'action',
        seat: S.seat,
        payload: action,
        extra: options.label || '提交 ' + JSON.stringify(action),
      });
      renderActions();
    } else {
      S.pending = false;
    }
  }

  function discardTile(tile) {
    if (!canDiscardNow()) {
      toast('现在不能打牌（不是你的回合，或状态未同步）');
      return;
    }
    submitAction({ kind: 'discard', tile: tile });
  }

  // ── 渲染 ─────────────────────────────────────────────────────

  const ZONES = {
    bottom: function () {
      return S.seat;
    },
    right: function () {
      return (S.seat + 1) % 4;
    },
    top: function () {
      return (S.seat + 2) % 4;
    },
    left: function () {
      return (S.seat + 3) % 4;
    },
  };

  const POS_LABEL = { bottom: '你', right: '下家', top: '对家', left: '上家' };

  function seatShort(seat) {
    const n = num(seat, -1);
    if (n < 0 || n > 3) return '—';
    return n === S.seat ? '你' : 'seat ' + n;
  }

  function seatWind(seat) {
    return WIND[(num(seat, 0) - num(S.dealer, 0) + 4) % 4];
  }

  function backCount(seat) {
    const melds = S.melds[seat] ? S.melds[seat].length : 0;
    const base = 13 - 3 * melds;
    const holding = S.turn === seat && S.phase === 'await_discard' ? 1 : 0;
    return Math.max(0, base + holding);
  }

  function render() {
    renderZone('bottom', dom.zoneBottom);
    renderZone('right', dom.zoneRight);
    renderZone('top', dom.zoneTop);
    renderZone('left', dom.zoneLeft);
    renderCenter();
    renderActions();
    updatePills();
  }

  function renderZone(position, container) {
    const seat = ZONES[position]();
    container.replaceChildren();
    container.classList.toggle('me', seat === S.seat);
    container.classList.toggle(
      'turn',
      S.turn === seat && (S.phase === 'await_draw' || S.phase === 'await_discard')
    );

    // 头部：谁 + 自风 + 庄 + 点数 + 立直
    const head = el('div', 'zone-head');
    head.appendChild(el('span', 'seat-name', POS_LABEL[position] + '（seat ' + seat + '）'));
    head.appendChild(el('span', 'seat-wind', seatWind(seat) + '家'));
    if (seat === S.dealer) head.appendChild(el('span', 'seat-role', '庄'));
    head.appendChild(el('span', 'seat-score', S.scores[seat] === undefined ? '—' : String(S.scores[seat])));
    if (S.riichi[seat]) head.appendChild(el('span', 'riichi-badge', '立直'));
    container.appendChild(head);

    // 副露
    if (S.melds[seat] && S.melds[seat].length) {
      const row = el('div', 'meld-row');
      S.melds[seat].forEach(function (meld) {
        const box = el('div', 'meld');
        box.appendChild(el('span', 'meld-label', meldLabel(meld.kind)));
        (meld.tiles || []).forEach(function (tile) {
          box.appendChild(tileEl(tile, { size: 'sm', title: meldLabel(meld.kind) + ' ' + tile }));
        });
        row.appendChild(box);
      });
      container.appendChild(row);
    }

    if (position === 'bottom') {
      container.appendChild(renderRiver(seat));
      container.appendChild(renderHand());
    } else {
      const backs = el('div', 'hand-backs');
      const count = backCount(seat);
      for (let i = 0; i < count; i += 1) backs.appendChild(el('span', 'back'));
      container.appendChild(backs);
      container.appendChild(renderRiver(seat));
    }
  }

  function meldLabel(kind) {
    const map = {
      chi: '吃',
      pon: '碰',
      kan: '杠',
      kan_daiminkan: '明杠',
      kan_shouminkan: '加杠',
      kan_ankan: '暗杠',
    };
    return map[kind] || kind;
  }

  function renderRiver(seat) {
    const grid = el('div', 'river-grid');
    const tiles = S.discards[seat] || [];
    tiles.forEach(function (tile, index) {
      const node = tileEl(tile, { size: 'sm', title: '牌河 seat ' + seat + '：' + tile });
      const isLast =
        S.lastDiscard &&
        S.lastDiscard.seat === seat &&
        S.lastDiscard.tile === tile &&
        index === tiles.length - 1;
      if (isLast) node.classList.add('last-discard');
      grid.appendChild(node);
    });
    return grid;
  }

  /** 手牌：排序显示，自摸牌单独放右侧。 */
  function renderHand() {
    const row = el('div', 'hand-row');
    if (!S.handKnown) {
      const note =
        S.source === 'events-only'
          ? '手牌未知：仅事件流模式（初始配牌不在事件里，见横幅）'
          : '未连接：填入服务器/令牌后点「连接」，或点「新建对局」';
      row.appendChild(el('span', 'hand-note', note));
      return row;
    }
    const hand = S.hand.slice();
    let drawn = null;
    if (S.drawn) {
      const index = hand.lastIndexOf(S.drawn);
      if (index >= 0) {
        hand.splice(index, 1);
        drawn = S.drawn;
      }
    }
    hand.sort(function (a, b) {
      return tileOrder(a) - tileOrder(b);
    });
    const clickable = canDiscardNow();
    hand.forEach(function (tile) {
      row.appendChild(handTile(tile, clickable));
    });
    if (drawn) {
      const node = handTile(drawn, clickable);
      node.classList.add('drawn');
      row.appendChild(node);
    }
    if (hand.length === 0 && !drawn) {
      row.appendChild(el('span', 'hand-note', '手牌为空（等待发牌 / 已和了）'));
    }
    return row;
  }

  function handTile(tile, clickable) {
    const node = tileEl(tile, {
      size: 'lg',
      clickable: clickable,
      title: clickable ? '打出 ' + tile : tile,
    });
    if (clickable) {
      node.dataset.tile = tile;
      node.addEventListener('click', function () {
        discardTile(tile);
      });
      node.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') {
          ev.preventDefault();
          discardTile(tile);
        }
      });
    }
    return node;
  }

  function renderCenter() {
    dom.center.replaceChildren();
    const wall = el('div', 'center-wall');
    wall.appendChild(el('div', 'center-wall-num', S.wallRemaining === undefined ? '—' : String(S.wallRemaining)));
    wall.appendChild(el('div', 'center-wall-label', '剩余牌数'));
    dom.center.appendChild(wall);

    const round = el('div', 'center-round');
    round.appendChild(el('span', 'chip', WIND[S.roundWind] + ' ' + S.kyoku + ' 局'));
    round.appendChild(el('span', 'chip', '本场 ' + S.honba));
    round.appendChild(el('span', 'chip', '立直棒 ' + S.riichiSticks));
    dom.center.appendChild(round);

    if (S.dora.length) {
      const dora = el('div', 'center-dora');
      dora.appendChild(el('span', 'center-dora-label', '宝牌指示'));
      S.dora.forEach(function (tile) {
        dora.appendChild(tileEl(tile, { size: 'sm', title: '宝牌指示 ' + tile }));
      });
      dom.center.appendChild(dora);
    }

    const turnText = S.turn === S.seat ? '你' : seatShort(S.turn);
    const phaseHint = PHASE_HINT[S.phase] || '';
    dom.center.appendChild(el('div', 'center-turn', '行动：' + turnText + phaseHint));
    dom.center.appendChild(el('div', 'center-phase', (S.phase || '—') + ' · ' + (PHASE_LABEL[S.phase] || '')));
  }

  function actionLabel(action) {
    const kind = action.kind;
    switch (kind) {
      case 'draw':
        return '摸牌';
      case 'pass':
        return '跳过（pass）';
      case 'riichi':
        return '立直';
      case 'tsumo':
        return '自摸和了';
      case 'ron':
        return '荣和';
      case 'kyuushu':
        return '九种九牌';
      case 'chi':
        return '吃 ' + (action.consumed || []).join(' ');
      case 'pon':
        return '碰 ' + (action.tile || '');
      case 'kan_daiminkan':
        return '大明杠 ' + (action.tile || '');
      case 'kan_shouminkan':
        return '加杠 ' + (action.tile || '');
      case 'kan_ankan':
        return '暗杠';
      default:
        return kind;
    }
  }

  function actionPayload(action) {
    const out = { kind: action.kind };
    if (action.tile) out.tile = action.tile;
    if (Array.isArray(action.consumed) && action.consumed.length) out.consumed = action.consumed;
    if (action.kind === 'riichi' && !out.tile) {
      out.tile = S.drawn || (S.hand.length ? S.hand[0] : undefined);
      if (!out.tile) delete out.tile;
    }
    return out;
  }

  const PLACEHOLDER_KINDS = [
    ['riichi', '立直'],
    ['tsumo', '自摸'],
    ['ron', '荣和'],
    ['chi', '吃'],
    ['pon', '碰'],
    ['kan_ankan', '杠'],
    ['pass', 'pass'],
  ];

  function renderActions() {
    dom.actions.replaceChildren();
    if (!S.gameId) {
      dom.actionHint.textContent = '填入对局与令牌后点「连接」';
      return;
    }
    // 响应窗口优先：只显示 responses 里的选项（吃/碰/杠/荣和/pass）
    if (S.responses) {
      const expired = Date.now() > S.responseDeadline;
      S.responses.options.forEach(function (option) {
        const primary = option.kind === 'ron' || option.kind === 'tsumo';
        const btn = el('button', 'btn' + (primary ? ' primary' : ''), responseOptionLabel(option));
        btn.type = 'button';
        btn.title = responseOptionTitle(option);
        btn.disabled = S.pending || expired;
        btn.addEventListener('click', function () {
          submitResponse(option);
        });
        dom.actions.appendChild(btn);
      });
      dom.actionHint.textContent = expired
        ? '响应超时，等待服务端按 pass 结算…'
        : '响应窗口：' + S.responseLeft + 's 内选一个（不选按 pass）';
      return;
    }
    const legal = Array.isArray(S.legal) ? S.legal : [];
    const present = {};
    legal.forEach(function (a) {
      if (a && a.kind) present[a.kind] = true;
    });

    legal.forEach(function (action) {
      if (!action || action.kind === 'discard') return; // 打牌走「点手牌」
      const btn = el('button', 'btn', actionLabel(action));
      btn.type = 'button';
      btn.title = '提交 ' + JSON.stringify(actionPayload(action));
      btn.addEventListener('click', function () {
        submitAction(actionPayload(action));
      });
      dom.actions.appendChild(btn);
    });

    // 占位按钮：服务端 M2 未接入时显示为禁用，说明「预留」
    PLACEHOLDER_KINDS.forEach(function (pair) {
      if (present[pair[0]]) return;
      const btn = el('button', 'btn tiny ghost', pair[1]);
      btn.type = 'button';
      btn.disabled = true;
      btn.title = '当前不在合法动作内（服务端 M2 接入后按 legal_actions 出现在左侧）';
      dom.actions.appendChild(btn);
    });

    if (S.pending) {
      dom.actionHint.textContent = '已提交，等待服务端事件…';
      return;
    }
    if (!S.handKnown) {
      dom.actionHint.textContent = '状态未同步：无法得知手牌与合法动作';
      return;
    }
    if (canDiscardNow()) {
      dom.actionHint.textContent = '轮到你：点击手牌打出（' + S.hand.length + ' 张）';
      return;
    }
    if (S.phase === 'round_end' || S.phase === 'game_end') {
      dom.actionHint.textContent = '本局/对局已结束';
      return;
    }
    dom.actionHint.textContent = '等待 ' + seatShort(S.turn) + ' 行动…';
  }

  // ── 思考条 ───────────────────────────────────────────────────

  function fmtAction(action) {
    if (!action) return '—';
    if (typeof action === 'string') return action;
    if (typeof action.kind === 'string') {
      if (action.kind === 'discard') return '打 ' + (action.tile || '?');
      if (action.kind === 'draw') return '摸牌';
      if (action.kind === 'pass') return 'pass';
      return actionLabel(action);
    }
    try {
      return JSON.stringify(action);
    } catch (err) {
      return String(action);
    }
  }

  /** 渲染 AI 思考条（真实事件 agent_thought：seat/agent/elapsed_ms/chosen/candidates）。 */
  function showThought(payload, seat) {
    const agent = payload.agent || payload.name || 'agent';
    const elapsed = payload.elapsed_ms !== undefined ? payload.elapsed_ms : payload.elapsedMs;
    const chosen = payload.chosen !== undefined ? payload.chosen : payload.action;
    const candidates = Array.isArray(payload.candidates) ? payload.candidates : [];
    dom.thoughtAgent.textContent = agent + ' · ' + seatShort(seat === undefined ? -1 : seat);
    let text = '思考 ' + (elapsed === undefined ? '?' : elapsed + 'ms');
    text += ' → ' + fmtChosen(chosen);
    if (candidates.length) {
      text +=
        ' · 候选 ' +
        candidates.length +
        '：' +
        candidates
          .slice(0, 5)
          .map(fmtCandidate)
          .join(' / ');
    }
    dom.thoughtText.textContent = text;
    dom.thoughtBar.classList.remove('hidden');
    dom.thoughtBar.classList.remove('demo');
    if (S.thoughtTimer) clearTimeout(S.thoughtTimer);
    S.thoughtTimer = setTimeout(function () {
      dom.thoughtBar.classList.add('hidden');
    }, 12000);
  }

  /** 思考条里的「选择」展示：字符串牌面 → 「打 9s」；对象 → 动作名。 */
  function fmtChosen(chosen) {
    if (chosen === undefined || chosen === null || chosen === '') return '—';
    if (typeof chosen === 'string') {
      if (/^r?[1-9][mps]$/i.test(chosen) || /^[ESWNPFC]$/.test(chosen)) return '打 ' + chosen;
      return fmtCandidate(chosen);
    }
    return fmtAction(chosen);
  }

  /** 服务端的 candidate 是 "discard 9s" 这类「动作名 + 牌」字符串，翻成人话。 */
  function fmtCandidate(text) {
    if (typeof text !== 'string') return fmtAction(text);
    const matched = /^([a-z_]+)\s*(.*)$/.exec(text);
    if (!matched) return text;
    const map = {
      discard: '打',
      draw: '摸',
      pass: 'pass',
      riichi: '立直',
      tsumo: '自摸',
      ron: '荣和',
      pon: '碰',
      chi: '吃',
      kan_daiminkan: '大明杠',
      kan_shouminkan: '加杠',
      kan_ankan: '暗杠',
      kyuushu: '九种九牌',
    };
    const label = map[matched[1]];
    if (!label) return text;
    return label + (matched[2] ? ' ' + matched[2] : '');
  }

  // ── 事件日志 ─────────────────────────────────────────────────

  function payloadSummary(kind, payload) {
    const p = payload || {};
    switch (kind) {
      case 'draw':
        return p.tile + ' · 余 ' + p.wall_remaining;
      case 'discard':
        return p.tile + (p.tsumogiri ? '（摸切）' : '') + (p.riichi_declared ? ' · 立直' : '');
      case 'round_start':
        return WIND[num(p.round_wind, 0)] + ' ' + num(p.kyoku, 1) + '局 · 本场 ' + num(p.honba, 0) + ' · 庄 seat ' + num(p.dealer, 0);
      case 'resumed':
        return 'from ' + p.from_seq + ' → ' + p.to_seq;
      case 'error':
        return (p.code || '') + ' ' + (p.message || '');
      case 'agent_thought':
        return (
          (p.agent || '') +
          ' ' +
          (p.elapsed_ms === undefined ? '' : p.elapsed_ms + 'ms') +
          ' → ' +
          fmtChosen(p.chosen)
        );
      case 'responses': {
        const options = Array.isArray(p.options) ? p.options : [];
        return options.length ? options.map(responseOptionLabel).join(' / ') : '（已忽略）';
      }
      case 'response_window_end':
        return 'seq ' + num(p.seq, 0);
      case 'chi':
      case 'pon':
      case 'kan': {
        const tiles = Array.isArray(p.tiles) ? p.tiles : [];
        return (p.meld_kind || p.kan_type || kind) + ' ' + tiles.join('') + (p.from === undefined ? '' : ' ← s' + p.from);
      }
      case 'tsumo':
      case 'ron':
        return (p.winning_tile || '') + ' ' + (p.han === undefined ? '' : p.han + ' 翻 ' + (p.fu || '') + ' 符');
      case 'dora_reveal':
        return '指示 ' + (p.indicator || '');
      default:
        try {
          const json = JSON.stringify(p);
          return json && json !== '{}' ? json.slice(0, 120) : '';
        } catch (err) {
          return '';
        }
    }
  }

  function appendLog(entry, historical) {
    const kind = entry.kind || 'event';
    const seq = num(entry.seq, 0);
    if (seq > 0 && seq <= S.logMaxSeq && !historical) return; // 重放去重
    if (seq > 0) S.logMaxSeq = Math.max(S.logMaxSeq, seq);

    const li = el('li', 'log-item k-' + kind);
    li.appendChild(el('span', 'log-seq', seq ? '#' + seq : '·'));
    li.appendChild(el('span', 'log-kind', kind));
    li.appendChild(el('span', 'log-seat', entry.seat === undefined || entry.seat < 0 ? '—' : 's' + entry.seat));
    li.appendChild(el('span', 'log-extra', entry.extra !== undefined ? entry.extra : payloadSummary(kind, entry.payload)));
    dom.eventLog.appendChild(li);
    S.logCount += 1;
    dom.eventCount.textContent = String(S.logCount);
    while (dom.eventLog.children.length > LOG_LIMIT) {
      dom.eventLog.removeChild(dom.eventLog.firstChild);
    }
    if (!S.logPaused) dom.eventLog.scrollTop = dom.eventLog.scrollHeight;
  }

  function logEvent(entry) {
    appendLog(entry, false);
  }

  /** 切换对局时清空日志与序号水位（否则会看到上一局的日志）。 */
  function resetLog() {
    dom.eventLog.replaceChildren();
    S.logCount = 0;
    S.logMaxSeq = 0;
    S.loggedMaxSeq = 0;
    S.historyLoaded = false;
    dom.eventCount.textContent = '0';
  }

  function fmtScores(scores) {
    if (!Array.isArray(scores)) return '—';
    return scores.join(' / ');
  }

  function toast(text) {
    setBanner(text, 'err');
    if (S.toastTimer) clearTimeout(S.toastTimer);
    S.toastTimer = setTimeout(function () {
      if (dom.banner.textContent === text) setBanner('');
    }, 4000);
  }

  // ── 新建对局 ─────────────────────────────────────────────────

  async function createGame() {
    readForm();
    if (!S.token) {
      setBanner('新建对局需要 JWT：先按 README 签发令牌并填入。', 'warn');
      return;
    }
    setStatus('建局中…', 'busy');
    try {
      const result = await apiJson('/api/v1/games', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ seed: Math.floor(Math.random() * 1000000) }),
      });
      const data = result.data;
      S.gameId = data.game_id;
      S.apiBase = result.base;
      S.source = result.base === '' ? 'same-origin' : 'direct';
      dom.gameId.value = S.gameId;
      S.handKnown = false;
      S.historyLoaded = false;
      S.loggedMaxSeq = 0;
      S.logMaxSeq = 0;
      S.lastEventSeq = 0;
      S.seq = 0;
      S._connectedGameId = '';
      S._logGameId = S.gameId;
      resetLog();
      setBanner(
        '已建局 ' +
          data.game_id +
          '（seed ' +
          data.seed +
          '，agents: ' +
          (data.agents || []).join(', ') +
          '）',
        'info'
      );
      logEvent({ kind: 'create', seat: -1, extra: data.game_id + ' seed=' + data.seed });
      saveSettings();
      await connect();
    } catch (err) {
      setStatus('建局失败', 'err');
      setBanner(
        '建局失败：' +
          (err && err.message ? err.message : err) +
          '。若报 CORS/NetworkError，说明页面与后端不同源，请用 `python web/serve.py` 打开页面。',
        'err'
      );
    }
  }

  // ── 事件绑定 / 启动 ──────────────────────────────────────────

  function bindEvents() {
    dom.form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      connect({ isReconnect: false });
    });
    dom.btnNew.addEventListener('click', createGame);
    dom.btnTokenToggle.addEventListener('click', function () {
      const hidden = dom.token.type === 'password';
      dom.token.type = hidden ? 'text' : 'password';
      dom.btnTokenToggle.textContent = hidden ? '隐藏' : '显示';
    });
    dom.btnDisconnect.addEventListener('click', disconnect);
    dom.btnClearLog.addEventListener('click', function () {
      dom.eventLog.replaceChildren();
      S.logCount = 0;
      dom.eventCount.textContent = '0';
    });
    dom.logPause.addEventListener('change', function () {
      S.logPaused = dom.logPause.checked;
    });
    dom.btnResync.addEventListener('click', async function () {
      if (!S.gameId) return;
      try {
        const result = await apiJson('/api/v1/games/' + encodeURIComponent(S.gameId));
        applyObservation(result.data);
        S.apiBase = result.base;
        S.source = result.base === '' ? 'same-origin' : 'direct';
        setBanner('已重新同步（seq ' + S.seq + '）', 'info');
        render();
      } catch (err) {
        setBanner('重新同步失败：' + (err && err.message ? err.message : err), 'err');
      }
    });
    dom.btnDrop.addEventListener('click', simulateDrop);
    window.addEventListener('beforeunload', function () {
      S.userClosed = true;
      closeSocket(true);
    });
  }

  function init() {
    cacheDom();
    bindEvents();
    // 验收 / 调试把手：浏览器 DevTools 里可用来模拟断线、强制 resume（README「手动验收」用到）
    window.__s11 = {
      state: S,
      simulateDrop: simulateDrop,
      reconnectNow: function () {
        connect({ isReconnect: true });
      },
      resumeFrom: function (seq) {
        send({ type: 'resume', last_seq: seq });
      },
      /** 响应窗口调试：返回当前 options / 按索引提交（README 验收用） */
      responseOptions: function () {
        return S.responses ? S.responses.options : [];
      },
      respond: function (index) {
        if (!S.responses) return null;
        const option = S.responses.options[index || 0];
        if (option) submitResponse(option);
        return option || null;
      },
      disconnect: disconnect,
    };
    const autoConnect = restoreSettings();
    render();
    setStatus('未连接', 'idle');
    if (autoConnect) {
      setBanner('已从 localStorage 恢复上次的对局与令牌，正在自动连接…', 'info');
      connect({ isReconnect: false });
    } else {
      setBanner(
        '填入服务器地址 / 令牌后点「连接」，或点「新建对局」由页面建一局（1 人类 + 3 AI）。令牌签发见 web/README.md。',
        'info'
      );
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
