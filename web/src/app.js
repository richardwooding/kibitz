// app.js — thin view layer. All protocol, crypto, and rules live in the Go
// WASM core; this file renders state and forwards user intents through the
// two-function bridge:
//   window.kibitz_send(json)   — UI → core (installed by the core)
//   window.kibitzOnEvent(json) — core → UI (defined here)
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const views = { home: $("view-home"), lobby: $("view-lobby"), table: $("view-table") };

  const state = {
    self: 0,
    role: "",        // host | player | spectator
    members: {},     // id -> role
    game: null,          // last chess.state payload
    selected: null,      // selected square
    selectedPiece: null, // FEN char of the piece on it
    drawPending: false,
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

  // ---- core → UI ----------------------------------------------------------

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
      renderStatus();
    },
    "session.closed"(e) {
      toast(e.reason === "host left" ? "The host closed the table." : `Session ended: ${e.reason || "connection lost"}`);
      setTimeout(() => location.replace(location.pathname), 1500);
    },
    roster(e) {
      state.members = e.members;
      renderMembers();
    },
    "chat.msg"(e) {
      appendChat(e.from, e.text);
    },
    "chess.state"(e) {
      state.game = e;
      state.drawPending = false;
      $("btn-agree-draw").classList.add("hidden");
      if (state.role === "host") show("table"); // first state = opponent arrived
      renderBoard();
      renderStatus();
    },
    "chess.drawOffered"(e) {
      if (isPlayer()) {
        state.drawPending = true;
        $("btn-agree-draw").classList.remove("hidden");
        toast("Draw offered — accept?");
      } else {
        toast("A draw was offered.");
      }
    },
    "chess.targets"(e) {
      if (e.from !== state.selected) return; // stale reply
      window.Board.setSelection($("board"), state.selected, e.targets, boardOpts());
    },
    error(e) {
      toast(e.message);
    },
  };

  window.kibitzOnEvent = (json) => {
    let e;
    try { e = JSON.parse(json); } catch { return; }
    const h = handlers[e.type];
    if (h) h(e);
  };

  // ---- rendering ----------------------------------------------------------

  function isPlayer() {
    const g = state.game;
    return g && (g.whiteId === state.self || g.blackId === state.self);
  }

  function myTurn() {
    return state.game && state.game.turnId === state.self && !gameOver();
  }

  function gameOver() {
    return state.game && state.game.outcome !== "*";
  }

  function boardOpts() {
    return {
      flipped: state.game && state.game.blackId === state.self,
      lastMove: state.game && state.game.lastUci,
    };
  }

  function renderBoard() {
    if (!state.game || !state.game.playing) return;
    state.selected = null;
    window.Board.render($("board"), state.game.fen, boardOpts());
  }

  function renderStatus() {
    const el = $("status-line");
    const g = state.game;
    if (!g || !g.playing) {
      el.textContent = "Waiting for the game to start…";
      return;
    }
    if (gameOver()) {
      const result = g.outcome === "1/2-1/2" ? "Draw" :
        (g.outcome === "1-0" ? "White wins" : "Black wins");
      el.textContent = `${result} — ${g.method}`;
      return;
    }
    const turnWhite = g.turnId === g.whiteId;
    const who = g.turnId === state.self ? "Your move" : (turnWhite ? "White to move" : "Black to move");
    el.textContent = who + (state.role === "spectator" ? " (you're kibitzing)" : "");
    $("btn-resign").classList.toggle("hidden", !isPlayer() || gameOver());
    $("btn-draw").classList.toggle("hidden", !isPlayer() || gameOver());
  }

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

  // ---- user input ---------------------------------------------------------

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

  $("btn-resign").addEventListener("click", () => {
    if (confirm("Resign the game?")) send({ type: "chess.resign" });
  });
  $("btn-draw").addEventListener("click", () => send({ type: "chess.offerDraw" }));
  $("btn-agree-draw").addEventListener("click", () => send({ type: "chess.agreeDraw" }));

  // Click-click move input: first click selects own piece (core supplies the
  // legal targets), second click on a target sends the move. Promotion is
  // auto-queen for now.
  window.Board.onSquareClick((sq, piece) => {
    if (!state.game || !state.game.playing || gameOver()) return;
    if (!isPlayer()) return;

    if (state.selected && state.selected !== sq) {
      const wasTarget = [...document.querySelectorAll(".sq.target")]
        .some((c) => c.dataset.sq === sq);
      if (wasTarget) {
        if (!myTurn()) { toast("Not your turn."); return; }
        const uci = state.selected + sq + promotionSuffix(state.selectedPiece, sq);
        state.selected = null;
        state.selectedPiece = null;
        send({ type: "chess.move", uci });
        window.Board.setSelection($("board"), null, [], boardOpts());
        return;
      }
    }

    // (Re)select: only own pieces.
    const mineIsWhite = state.game.whiteId === state.self;
    if (piece && window.Board.pieceIsWhite(piece) === mineIsWhite) {
      state.selected = sq;
      state.selectedPiece = piece;
      send({ type: "chess.targets", from: sq, id: Date.now() });
    } else {
      state.selected = null;
      state.selectedPiece = null;
      window.Board.setSelection($("board"), null, [], boardOpts());
    }
  });

  // Auto-queen: only a pawn reaching the far rank needs a suffix.
  function promotionSuffix(piece, to) {
    if (piece === "P" && to[1] === "8") return "q";
    if (piece === "p" && to[1] === "1") return "q";
    return "";
  }

  // ---- boot the core ------------------------------------------------------

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
