// checkersboard.js — the checkers module: 8×8 board over the 32-dark-square
// state, click-click path building against the core's legal move set
// (multi-jumps by prefix filtering, the backgammon trick).
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.checkers = { label: "⛀ Checkers", paneId: "game-checkers", create };

  // dark-square index ↔ (row, col)
  const rowOf = (s) => Math.floor(s / 4);
  const colOf = (s) => (rowOf(s) % 2 === 0 ? 2 * (s % 4) + 1 : 2 * (s % 4));

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let path = []; // square indices being built
    let visible = false;

    const isPlayer = () => g && (g.p1Id === ctx.self() || g.p2Id === ctx.self());
    const myTurn = () => g && g.turnId === ctx.self();
    const over = () => g && g.outcome !== "";

    function candidates() {
      return (g.legal || []).filter((m) =>
        m.length >= path.length && path.every((s, i) => m[i] === s));
    }
    function nextSquares() {
      const at = path.length;
      const out = new Set();
      for (const m of candidates()) {
        if (m.length > at) out.add(m[at]);
      }
      return out;
    }
    function sources() {
      const out = new Set();
      for (const m of g.legal || []) out.add(m[0]);
      return out;
    }

    function onSquare(s) {
      if (!g || !myTurn() || over() || !isPlayer()) return;
      if (path.length > 0 && nextSquares().has(s)) {
        path.push(s);
        const cands = candidates();
        if (cands.some((m) => m.length === path.length)) {
          // Complete legal move (paths are maximal, so equality = done).
          send({ type: "checkers.move", path });
          path = [];
        }
        render();
        return;
      }
      path = sources().has(s) ? [s] : [];
      render();
    }

    function render() {
      if (!visible || !g) return;
      const statusEl = $("checkers-status");
      if (!g.playing) {
        statusEl.textContent = "Waiting for the game to start…";
        return;
      }
      if (over()) {
        statusEl.textContent = g.outcome;
      } else {
        const dark = g.turnId === g.p1Id;
        statusEl.textContent = (myTurn() ? "Your move" :
          (isPlayer() ? "Opponent's move" : (dark ? "Dark to move" : "Light to move"))) +
          (isPlayer() ? ` · you are ${g.p1Id === ctx.self() ? "dark" : "light"}` : "");
      }
      $("checkers-resign").classList.toggle("hidden", !isPlayer() || over());
      $("checkers-draw").classList.toggle("hidden", !isPlayer() || over());

      const el = $("checkers-board");
      el.innerHTML = "";
      const hiNext = path.length ? nextSquares() : new Set();
      const hiSrc = path.length === 0 && myTurn() ? sources() : new Set();
      // The moving player sees their own side at the bottom: P1 (dark)
      // starts on rows 0-2, so flip for P1.
      const flip = g.p1Id === ctx.self();
      for (let vr = 0; vr < 8; vr++) {
        for (let vc = 0; vc < 8; vc++) {
          const r = flip ? 7 - vr : vr;
          const c = flip ? 7 - vc : vc;
          const cell = document.createElement("button");
          cell.type = "button";
          const dark = (r + c) % 2 === 1;
          cell.className = "ck-cell " + (dark ? "dark" : "light");
          if (dark) {
            const s = r % 2 === 0 ? r * 4 + (c - 1) / 2 : r * 4 + c / 2;
            const v = g.board[s];
            if (v !== 0) {
              const piece = document.createElement("span");
              piece.className = "ck-piece " + (v > 0 ? "p1" : "p2");
              piece.textContent = Math.abs(v) === 2 ? "♛" : "";
              cell.appendChild(piece);
            }
            if (path.includes(s)) cell.classList.add("selected");
            if (hiNext.has(s)) cell.classList.add("target");
            if (hiSrc.has(s)) cell.classList.add("source");
            cell.addEventListener("click", () => onSquare(s));
          }
          el.appendChild(cell);
        }
      }
    }

    $("checkers-resign").addEventListener("click", () => {
      if (confirm("Resign checkers?")) send({ type: "checkers.resign" });
    });
    $("checkers-draw").addEventListener("click", () => send({ type: "checkers.offerDraw" }));
    $("checkers-agree-draw").addEventListener("click", () => send({ type: "checkers.agreeDraw" }));

    return {
      onEvent(type, e) {
        switch (type) {
          case "checkers.state":
            g = e;
            path = [];
            $("checkers-agree-draw").classList.add("hidden");
            render();
            break;
          case "checkers.drawOffered":
            if (isPlayer()) {
              $("checkers-agree-draw").classList.remove("hidden");
              toast("Draw offered — accept?");
            }
            break;
        }
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) return { status: "over", detail: g.outcome };
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
