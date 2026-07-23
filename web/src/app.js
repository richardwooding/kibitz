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
    activeGame: null, // module id when a pane is open; null = picker
  };

  function show(name) {
    for (const [k, v] of Object.entries(views)) v.classList.toggle("hidden", k !== name);
  }

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
  };
  const games = {}; // id -> instantiated module
  for (const [id, def] of Object.entries(window.GameModules || {})) {
    games[id] = { ...def, ...def.create(ctx) };
  }

  function openGame(id) {
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

  $("btn-back").addEventListener("click", closeGame);

  function renderPicker() {
    const el = $("game-picker");
    el.innerHTML = "";
    const canStart = state.role === "host" || state.role === "player";
    for (const [id, mod] of Object.entries(games)) {
      const card = document.createElement("div");
      card.className = "game-card";
      const title = document.createElement("div");
      title.className = "game-title";
      title.textContent = mod.label;
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
      } else {
        badge.textContent = "not started";
        if (canStart) card.appendChild(actionButton("+ Start", id));
      }
      card.appendChild(badge);
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
      $("home-status").textContent = "";
      const phrase = decodeURIComponent(location.hash.slice(1));
      if (phrase) {
        $("home-status").textContent = `joining ${phrase}…`;
        send({ type: "join", phrase });
      }
    },
    "session.created"(e) {
      state.self = e.self;
      state.role = "host";
      $("lobby-phrase").textContent = e.phrase;
      $("lobby-url").value = e.url;
      if (e.qr) $("lobby-qr").src = "data:image/png;base64," + e.qr;
      show("lobby");
    },
    "session.joined"(e) {
      state.self = e.self;
      state.role = e.role;
      show("table");
      renderPicker();
    },
    "session.closed"(e) {
      toast(e.reason === "host left" ? "The host closed the table." : `Session ended: ${e.reason || "connection lost"}`);
      setTimeout(() => location.replace(location.pathname), 1500);
    },
    roster(e) {
      state.members = e.members;
      renderMembers();
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
      }
    }
  };

  // ---- roster + chat --------------------------------------------------------

  function renderMembers() {
    const el = $("members");
    el.innerHTML = "";
    const names = { host: "♔ host", player: "♟ player", spectator: "👁 kibitzer" };
    for (const [id, role] of Object.entries(state.members)) {
      const div = document.createElement("div");
      div.className = "member";
      div.textContent = `${names[role] || role} #${id}` + (Number(id) === state.self ? " (you)" : "");
      el.appendChild(div);
    }
  }

  function appendChat(from, text) {
    const log = $("chat-log");
    const div = document.createElement("div");
    div.className = "chat-msg" + (from === state.self ? " own" : "");
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = from === state.self ? "you" : `#${from}`;
    div.appendChild(who);
    div.appendChild(document.createTextNode(" " + text));
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
  }

  // ---- user input -----------------------------------------------------------

  $("btn-create").addEventListener("click", () => {
    $("btn-create").disabled = true;
    $("home-status").textContent = "opening a table…";
    send({ type: "create" });
  });

  $("btn-join").addEventListener("click", joinFromInput);
  $("join-phrase").addEventListener("keydown", (e) => {
    if (e.key === "Enter") joinFromInput();
  });
  function joinFromInput() {
    const phrase = $("join-phrase").value.trim();
    if (phrase) send({ type: "join", phrase });
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
