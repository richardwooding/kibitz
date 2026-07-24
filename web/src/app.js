// app.js — session flow, chat, roster, and the game picker/router. All
// protocol, crypto, and rules live in the Go WASM core; per-game rendering
// and input live in each game's module file (board.js, bgboard.js, …),
// registered via window.GameModules. The bridge:
//   window.kibitz_send(json)   — UI → core (installed by the core)
//   window.kibitzOnEvent(json) — core → UI (defined here)
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const views = { home: $("view-home"), lobby: $("view-lobby"), table: $("view-table") };

  const state = {
    self: 0,
    role: "", // host | player | spectator
    members: {},
    names: {}, // id -> screen name (from the ctl roster)
    activeGame: null, // module id when a pane is open; null = picker
    solo: false, // local hot-seat (no relay); the user drives both sides
    inSession: false, // pushed a history entry for the session (lobby/table)
  };

  // displayName returns a participant's screen name, "you" for self, or a
  // "#id" fallback before a name is known.
  function displayName(id) {
    if (id === state.self) return "you";
    return state.names[id] || ("#" + id);
  }

  // seatedPlayers returns the two players (host + player roles), ascending;
  // spectators excluded. The pair is the same across all games at the table.
  function seatedPlayers() {
    const ids = [];
    for (const [idStr, role] of Object.entries(state.members)) {
      if (role === "host" || role === "player") ids.push(Number(idStr));
    }
    return ids.sort((a, b) => a - b);
  }

  // matchupText is "you vs Ada" (when you're a player) or "Ada vs Bo".
  function matchupText() {
    const ps = seatedPlayers();
    if (ps.length < 2) return "";
    if (ps.includes(state.self)) {
      return "you vs " + displayName(ps.find((id) => id !== state.self));
    }
    return displayName(ps[0]) + " vs " + displayName(ps[1]);
  }

  function renderLobbyName() {
    const n = state.names[state.self] || document.getElementById("display-name").value.trim();
    $("lobby-you").textContent = n
      ? `You're hosting as ${n}.`
      : "You're hosting anonymously — set a name on the home screen next time.";
  }

  function show(name) {
    for (const [k, v] of Object.entries(views)) v.classList.toggle("hidden", k !== name);
  }

  // ---- navigation: home → table(picker) → game, mirrored by the OS/browser
  // Back button via the History API. Each level's "up" is one popstate.
  function pushSession() {
    if (state.inSession) return;
    state.inSession = true;
    history.pushState({ k: "session" }, "");
  }
  // Leaving a session (solo or networked) reloads to a clean home: it tears down
  // the core + session/loopback and drops any invite #phrase. Reload is the
  // simplest bulletproof reset (board modules hold state with no reset hook).
  function leaveToHome() {
    location.replace(location.pathname);
  }
  function leaveSession() {
    if (!state.solo) {
      const msg = state.role === "host" ? "Leave and close the table?" : "Leave the table?";
      if (!confirm(msg)) return;
    }
    leaveToHome();
  }
  window.addEventListener("popstate", () => {
    if (state.activeGame) { closeGame(); return; }        // game pane → picker
    if (views.home.classList.contains("hidden")) leaveToHome(); // in a session → home
    // already home: nothing to do
  });

  function send(obj) {
    if (window.kibitz_send) window.kibitz_send(JSON.stringify(obj));
  }

  let toastTimer = null;
  function toast(msg) {
    const el = $("toast");
    el.textContent = msg;
    el.classList.remove("hidden");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add("hidden"), 4000);
  }

  // ---- game modules ---------------------------------------------------------

  const ctx = {
    $, send, toast,
    self: () => state.self,
    role: () => state.role,
    name: displayName, // game modules label opponents by screen name
    solo: () => state.solo, // hot-seat: modules enable input for both sides
  };
  const games = {}; // id -> instantiated module
  for (const [id, def] of Object.entries(window.GameModules || {})) {
    games[id] = { ...def, ...def.create(ctx) };
  }

  function openGame(id) {
    if (state.activeGame !== id) history.pushState({ k: "game", id }, "");
    state.activeGame = id;
    $("game-picker").classList.add("hidden");
    $("game-pane").classList.remove("hidden");
    for (const [gid, mod] of Object.entries(games)) {
      $(mod.paneId).classList.toggle("hidden", gid !== id);
      mod.setVisible(gid === id);
    }
  }

  function closeGame() {
    state.activeGame = null;
    $("game-pane").classList.add("hidden");
    $("game-picker").classList.remove("hidden");
    for (const mod of Object.values(games)) mod.setVisible(false);
    renderPicker();
  }

  // Back-button controls funnel through history so in-app and OS Back agree.
  $("btn-back").addEventListener("click", () => history.back()); // game → picker
  $("btn-leave").addEventListener("click", leaveSession);        // table → home
  $("btn-cancel").addEventListener("click", leaveSession);       // lobby → home

  function renderPicker() {
    const el = $("game-picker");
    el.innerHTML = "";
    const canStart = state.role === "host" || state.role === "player";
    for (const [id, mod] of Object.entries(games)) {
      const card = document.createElement("div");
      card.className = "game-card";
      const title = document.createElement("div");
      title.className = "game-title";
      // Split "🔴 Connect Four" into a big icon + the name so the glyph is legible.
      const sp = mod.label.indexOf(" ");
      const iconEl = document.createElement("span");
      iconEl.className = "game-icon";
      iconEl.textContent = sp > 0 ? mod.label.slice(0, sp) : mod.label;
      const nameEl = document.createElement("span");
      nameEl.textContent = sp > 0 ? mod.label.slice(sp + 1) : "";
      title.append(iconEl, nameEl);
      card.appendChild(title);

      const info = mod.card();
      const badge = document.createElement("div");
      badge.className = "game-badge " + info.status;
      if (info.status === "live") {
        badge.textContent = info.myTurn ? "● your turn" : "○ in play";
        card.classList.add("clickable");
        card.addEventListener("click", () => openGame(id));
      } else if (info.status === "over") {
        badge.textContent = info.detail || "finished";
        card.classList.add("clickable");
        card.addEventListener("click", () => openGame(id));
        if (canStart) card.appendChild(actionButton("Rematch", id));
      } else if (state.solo && id === "battleship") {
        // Battleship's simultaneous fleet placement isn't supported in hot-seat.
        badge.textContent = "two players";
        const note = document.createElement("div");
        note.className = "game-matchup";
        note.textContent = "Invite a friend to play";
        card.appendChild(note);
      } else {
        badge.textContent = "not started";
        if (canStart) card.appendChild(actionButton("+ Start", id));
      }
      card.appendChild(badge);
      // Who's playing (live/finished games) — same pair across the table.
      if (info.status === "live" || info.status === "over") {
        const vs = matchupText();
        if (vs) {
          const m = document.createElement("div");
          m.className = "game-matchup";
          m.textContent = vs;
          card.appendChild(m);
        }
      }
      el.appendChild(card);
    }
  }

  function actionButton(label, gameID) {
    const b = document.createElement("button");
    b.className = "start-btn";
    b.textContent = label;
    b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      send({ type: "game.start", game: gameID });
    });
    return b;
  }

  // ---- core → UI ------------------------------------------------------------

  const handlers = {
    "core.ready"() {
      $("btn-create").disabled = false;
      $("btn-join").disabled = false;
      $("join-phrase").disabled = false;
      $("btn-solo").disabled = false;
      $("home-status").textContent = "";
      // Arriving via a share link: switch the home screen into "invited"
      // mode — a prominent invite banner, name field, and a big Join — rather
      // than auto-joining (which robbed link-openers of a name) or leaving the
      // join buried under "Start a table".
      const phrase = decodeURIComponent(location.hash.slice(1));
      if (phrase) {
        $("join-phrase").value = phrase;
        $("invite-phrase").textContent = phrase;
        $("invite-banner").classList.remove("hidden");
        $("view-home").classList.add("invited");
        $("btn-join").textContent = "Join game";
        $("btn-create").textContent = "or start your own table";
        $("display-name").focus();
      }
    },
    "session.created"(e) {
      state.self = e.self;
      state.role = "host";
      $("lobby-phrase").textContent = e.phrase;
      $("lobby-url").value = e.url;
      if (e.qr) $("lobby-qr").src = "data:image/png;base64," + e.qr;
      renderLobbyName();
      pushSession();
      show("lobby");
    },
    "session.joined"(e) {
      state.self = e.self;
      state.role = e.role;
      state.solo = !!e.solo;
      views.table.classList.toggle("solo", state.solo);
      pushSession();
      show("table");
      renderPicker();
    },
    "session.closed"(e) {
      toast(e.reason === "host left" ? "The host closed the table." : `Session ended: ${e.reason || "connection lost"}`);
      setTimeout(() => location.replace(location.pathname), 1500);
    },
    roster(e) {
      state.members = e.members;
      state.names = {};
      for (const [id, n] of Object.entries(e.names || {})) state.names[Number(id)] = n;
      renderMembers();
      renderLobbyName(); // no-op visually unless the lobby is showing
      // Names may have just arrived — refresh the open game's labels.
      if (state.activeGame && games[state.activeGame]) {
        games[state.activeGame].setVisible(true);
      }
      // The host's lobby → table transition: someone arrived.
      if (state.role === "host" && Object.keys(e.members).length > 1) {
        show("table");
        renderPicker();
      }
    },
    "chat.msg"(e) {
      appendChat(e.from, e.text);
    },
    error(e) {
      toast(e.message);
    },
  };

  window.kibitzOnEvent = (json) => {
    let e;
    try { e = JSON.parse(json); } catch { return; }
    const h = handlers[e.type];
    if (h) { h(e); return; }
    // "<gameId>.xyz" events route to that game's module.
    const dot = e.type.indexOf(".");
    if (dot > 0) {
      const mod = games[e.type.slice(0, dot)];
      if (mod) {
        mod.onEvent(e.type, e);
        renderPicker();
        updateTurnCue();
      }
    }
  };

  // ---- mute toggle + background turn cue -------------------------------------

  function syncMuteIcon() {
    const b = $("btn-mute");
    const muted = window.fx && window.fx.sound.isMuted();
    b.textContent = muted ? "🔇" : "🔊";
    b.title = muted ? "Unmute sound" : "Mute sound";
  }
  $("btn-mute").addEventListener("click", () => {
    if (window.fx) window.fx.sound.toggleMute();
    syncMuteIcon();
  });
  syncMuteIcon();

  // When a game needs the local player's action and the tab is hidden, flag
  // it in the title bar (restored on return). A gentle "it's your move" nudge.
  let baseTitle = document.title;
  function updateTurnCue() {
    const myMove = Object.values(games).some((m) => {
      const c = m.card();
      return c.status === "live" && c.myTurn;
    });
    if (document.hidden && myMove) document.title = "● your turn · kibitz";
    else document.title = baseTitle;
  }
  document.addEventListener("visibilitychange", updateTurnCue);

  // ---- roster + chat --------------------------------------------------------

  function renderMembers() {
    const el = $("members");
    el.innerHTML = "";
    const roleLabel = { host: "♔", player: "♟", spectator: "👁" };
    const roleName = { host: "host", player: "player", spectator: "spectator" };
    for (const [idStr, role] of Object.entries(state.members)) {
      const id = Number(idStr);
      const label = state.names[id] || ("#" + id);
      const div = document.createElement("div");
      div.className = "member";
      const icon = document.createElement("span");
      icon.className = "role-icon";
      icon.textContent = roleLabel[role] || "";
      icon.title = roleName[role] || role;
      div.append(icon, document.createTextNode(label + (id === state.self ? " (you)" : "")));
      el.appendChild(div);
    }
  }

  function appendChat(from, text) {
    const log = $("chat-log");
    const div = document.createElement("div");
    div.className = "chat-msg" + (from === state.self ? " own" : "");
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = displayName(from);
    div.appendChild(who);
    div.appendChild(document.createTextNode(" " + text));
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
  }

  // ---- user input -----------------------------------------------------------

  // Screen name: remembered across visits, sent with create/join.
  const nameInput = $("display-name");
  nameInput.value = localStorage.getItem("kibitz.name") || "";
  const myName = () => {
    const n = nameInput.value.trim().slice(0, 24);
    localStorage.setItem("kibitz.name", n);
    return n;
  };

  $("btn-create").addEventListener("click", () => {
    $("btn-create").disabled = true;
    $("home-status").textContent = "opening a table…";
    send({ type: "create", name: myName() });
  });

  $("btn-solo").addEventListener("click", () => {
    $("btn-solo").disabled = true;
    $("home-status").textContent = "setting up a solo game…";
    send({ type: "solo", name: myName() });
  });

  $("btn-join").addEventListener("click", joinFromInput);
  $("join-phrase").addEventListener("keydown", (e) => {
    if (e.key === "Enter") joinFromInput();
  });
  // Enter in the name field joins too, once a phrase is present (e.g. after
  // arriving via a share link with the phrase pre-filled).
  $("display-name").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && $("join-phrase").value.trim()) joinFromInput();
  });
  function joinFromInput() {
    const phrase = $("join-phrase").value.trim();
    if (phrase) {
      $("home-status").textContent = `joining ${phrase}…`;
      send({ type: "join", phrase, name: myName() });
    }
  }

  $("btn-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("lobby-url").value);
      toast("Link copied.");
    } catch {
      $("lobby-url").select();
      toast("Press ⌘C / Ctrl-C to copy.");
    }
  });

  $("chat-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const input = $("chat-input");
    const text = input.value.trim();
    if (text) {
      send({ type: "chat.say", text });
      input.value = "";
    }
  });

  // ---- version badge (all screens) -----------------------------------------

  fetch("version")
    .then((r) => (r.ok ? r.text() : Promise.reject()))
    .then((v) => {
      v = v.trim();
      if (!v) return;
      const el = $("version-badge");
      el.textContent = v;
      el.classList.remove("hidden");
    })
    .catch(() => {}); // no relay (e.g. opened as a file) → leave hidden

  // ---- boot the core --------------------------------------------------------

  (async () => {
    if (typeof Go === "undefined") {
      $("home-status").textContent = "wasm_exec.js missing — run `make wasm`";
      return;
    }
    try {
      const go = new Go();
      const result = await WebAssembly.instantiateStreaming(fetch("kibitz.wasm"), go.importObject);
      go.run(result.instance);
    } catch (err) {
      $("home-status").textContent = "couldn't load the core: " + err;
    }
  })();
})();
