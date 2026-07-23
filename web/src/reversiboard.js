// reversiboard.js — the reversi module: 8×8 green grid, legal-move dots,
// click to place. Passes are computed by the core; the UI just toasts them.
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.reversi = { label: "⚪ Reversi", paneId: "game-reversi", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let visible = false;
    let lastPassToasted = false;

    const isPlayer = () => g && (g.p1Id === ctx.self() || g.p2Id === ctx.self());
    const myTurn = () => g && g.turnId === ctx.self();
    const over = () => g && g.outcome !== "";

    function render() {
      if (!visible || !g) return;
      const statusEl = $("reversi-status");
      if (!g.playing) {
        statusEl.textContent = "Waiting for the game to start…";
        return;
      }
      const score = ` · ⚫${g.black} ⚪${g.white}`;
      if (over()) {
        statusEl.textContent = g.outcome + score;
      } else {
        const blackTurn = g.turnId === g.p1Id;
        statusEl.textContent = (myTurn() ? "Your move" :
          (isPlayer() ? "Opponent's move" : (blackTurn ? "Black to move" : "White to move"))) +
          score + (isPlayer() ? ` · you are ${g.p1Id === ctx.self() ? "⚫" : "⚪"}` : "");
      }
      $("reversi-resign").classList.toggle("hidden", !isPlayer() || over());

      const el = $("reversi-board");
      el.innerHTML = "";
      const legal = new Set(myTurn() ? (g.legal || []) : []);
      for (let sq = 0; sq < 64; sq++) {
        const cell = document.createElement("button");
        cell.type = "button";
        cell.className = "rv-cell";
        const v = g.board[sq];
        if (v !== 0) {
          const disc = document.createElement("span");
          disc.className = "rv-disc " + (v > 0 ? "black" : "white");
          cell.appendChild(disc);
        }
        if (sq === g.lastSq) cell.classList.add("last");
        if (legal.has(sq)) {
          cell.classList.add("legal");
          cell.addEventListener("click", () => send({ type: "reversi.place", sq }));
        }
        el.appendChild(cell);
      }
    }

    $("reversi-resign").addEventListener("click", () => {
      if (confirm("Resign reversi?")) send({ type: "reversi.resign" });
    });

    return {
      onEvent(type, e) {
        if (type !== "reversi.state") return;
        g = e;
        if (g.passed && !lastPassToasted) {
          toast(myTurn() ? "Opponent had no move — you go again." : "No legal move — turn passed.");
        }
        lastPassToasted = g.passed;
        render();
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
