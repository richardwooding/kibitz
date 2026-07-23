// c4board.js — the Connect Four module: 7×6 grid, click a column to drop.
(() => {
  "use strict";

  const COLS = 7, ROWS = 6;

  window.GameModules = window.GameModules || {};
  window.GameModules.connect4 = { label: "🔴 Connect Four", paneId: "game-c4", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;

    const isPlayer = () => g && (g.p1Id === ctx.self() || g.p2Id === ctx.self());
    const myTurn = () => g && g.turnId === ctx.self();
    const over = () => g && g.outcome !== "";

    function render() {
      if (!visible || !g) return;
      const statusEl = $("c4-status");
      if (!g.playing) {
        statusEl.textContent = "Waiting for the game to start…";
        return;
      }
      if (over()) {
        statusEl.textContent = g.outcome;
      } else {
        const mine = g.turnId === ctx.self();
        const color = g.turnId === g.p1Id ? "🔴" : "🟡";
        statusEl.textContent = mine ? `Your move ${color}` :
          (isPlayer() ? `Opponent's move ${color}` : `${color} to move`);
        if (isPlayer()) {
          statusEl.textContent += ` · you are ${g.p1Id === ctx.self() ? "🔴" : "🟡"}`;
        }
      }
      $("c4-resign").classList.toggle("hidden", !isPlayer() || over());

      const el = $("c4-board");
      el.innerHTML = "";
      const win = new Set(g.winCells || []);
      // Draw top row first: row = ROWS-1 down to 0.
      for (let row = ROWS - 1; row >= 0; row--) {
        for (let col = 0; col < COLS; col++) {
          const idx = col * ROWS + row;
          const cell = document.createElement("button");
          cell.type = "button";
          cell.className = "c4-cell";
          const v = g.board[idx];
          if (v === 1) cell.classList.add("red");
          if (v === 2) cell.classList.add("yellow");
          if (win.has(idx)) cell.classList.add("win");
          cell.addEventListener("click", () => {
            if (myTurn() && !over()) send({ type: "c4.drop", col });
          });
          el.appendChild(cell);
        }
      }
    }

    $("c4-resign").addEventListener("click", () => {
      if (confirm("Resign Connect Four?")) send({ type: "c4.resign" });
    });

    return {
      onEvent(type, e) {
        if (type === "connect4.state") {
          g = e;
          render();
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
