// bsboard.js — the battleship module: fleet placement (manual + randomize)
// and the two grids (their waters / your fleet). All crypto and rules live
// in the core; this file draws and forwards intents. Sunk ships and cheat
// flags arrive via state — they are derived/verified by every client.
(() => {
  "use strict";

  const SHIPS = [
    { id: 1, name: "Carrier", len: 5 },
    { id: 2, name: "Battleship", len: 4 },
    { id: 3, name: "Cruiser", len: 3 },
    { id: 4, name: "Submarine", len: 3 },
    { id: 5, name: "Destroyer", len: 2 },
  ];

  window.GameModules = window.GameModules || {};
  window.GameModules.battleship = { label: "🚢 Battleship", paneId: "game-bs", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let visible = false;
    // placement state
    let fleet = new Array(100).fill(0);
    let placing = null; // ship id being placed
    let origin = null;  // first click

    const mySide = () => (g.p1Id === ctx.self() ? 0 : (g.p2Id === ctx.self() ? 1 : -1));
    const isPlayer = () => g && mySide() >= 0;
    const myTurn = () => g && g.turnId === ctx.self();

    // ---- placement ----------------------------------------------------------

    function placedIds() {
      const ids = new Set();
      for (const v of fleet) if (v) ids.add(v);
      return ids;
    }

    function tryPlace(shipId, a, b) {
      const len = SHIPS.find((s) => s.id === shipId).len;
      const ax = a % 10, ay = Math.floor(a / 10);
      const bx = b % 10, by = Math.floor(b / 10);
      let cells = [];
      if (ay === by) {
        const x0 = Math.min(ax, bx);
        if (x0 + len > 10) return null;
        for (let i = 0; i < len; i++) cells.push(ay * 10 + x0 + i);
      } else if (ax === bx) {
        const y0 = Math.min(ay, by);
        if (y0 + len > 10) return null;
        for (let i = 0; i < len; i++) cells.push((y0 + i) * 10 + ax);
      } else {
        return null;
      }
      if (cells.some((c) => fleet[c] !== 0 && fleet[c] !== shipId)) return null;
      return cells;
    }

    function randomizeFleet() {
      fleet = new Array(100).fill(0);
      for (const ship of SHIPS) {
        for (let attempt = 0; attempt < 500; attempt++) {
          const horizontal = Math.random() < 0.5;
          const x = Math.floor(Math.random() * (horizontal ? 10 - ship.len + 1 : 10));
          const y = Math.floor(Math.random() * (horizontal ? 10 : 10 - ship.len + 1));
          const cells = [];
          for (let i = 0; i < ship.len; i++) {
            cells.push(horizontal ? y * 10 + x + i : (y + i) * 10 + x);
          }
          if (cells.every((c) => fleet[c] === 0)) {
            for (const c of cells) fleet[c] = ship.id;
            break;
          }
        }
      }
      placing = null;
      origin = null;
    }

    function onPlaceCell(cell) {
      if (!placing) return;
      if (origin === null) {
        origin = cell;
        render();
        return;
      }
      const cells = tryPlace(placing, origin, cell);
      if (!cells) {
        toast("Doesn't fit there — pick a cell in line with the first.");
        origin = null;
        render();
        return;
      }
      // Clear any previous placement of this ship, then paint.
      fleet = fleet.map((v) => (v === placing ? 0 : v));
      for (const c of cells) fleet[c] = placing;
      placing = null;
      origin = null;
      render();
    }

    // ---- rendering ----------------------------------------------------------

    function grid(el, cellFn, clickFn) {
      el.innerHTML = "";
      for (let cell = 0; cell < 100; cell++) {
        const div = document.createElement("button");
        div.type = "button";
        div.className = "bs-cell " + cellFn(cell);
        if (clickFn) div.addEventListener("click", () => clickFn(cell));
        el.appendChild(div);
      }
    }

    function render() {
      if (!visible || !g) return;
      const statusEl = $("bs-status");
      const placingPane = $("bs-placing");
      const playPane = $("bs-playing");

      if (!g.playing) {
        statusEl.textContent = "Waiting for the game to start…";
        placingPane.classList.add("hidden");
        playPane.classList.add("hidden");
        return;
      }

      const side = mySide();
      if (g.phase === "placing" && isPlayer() && !g.committed[side]) {
        statusEl.textContent = "Place your fleet — nobody can see it, and the commitment makes lying impossible.";
        placingPane.classList.remove("hidden");
        playPane.classList.add("hidden");
        renderPlacement();
        return;
      }
      placingPane.classList.add("hidden");
      playPane.classList.remove("hidden");

      if (g.phase === "placing") {
        statusEl.textContent = isPlayer() ? "Fleet locked in — waiting for your opponent…"
          : "Players are placing their fleets…";
      } else if (g.phase === "shooting") {
        statusEl.textContent = myTurn() ? "Your shot — click their waters"
          : (isPlayer() ? "Incoming…" : "Kibitzing");
      } else {
        statusEl.textContent = g.outcome;
        if (g.cheatBy) statusEl.textContent += ` (participant ${g.cheatBy})`;
      }
      $("bs-resign").classList.toggle("hidden", !isPlayer() || g.phase === "over" || g.phase === "validating");

      const oppSide = side >= 0 ? 1 - side : 1;
      const ownSide = side >= 0 ? side : 0;
      $("bs-their-label").textContent = side >= 0 ? "Their waters" : "Player 2's board";
      $("bs-own-label").textContent = side >= 0 ? "Your fleet" : "Player 1's board";
      const sunkTheirs = (g.sunk && g.sunk[oppSide]) || [];

      // Their waters: my shots (reveals of the opponent board).
      grid($("bs-their"), (cell) => {
        const v = g.reveals[oppSide][cell];
        if (v === -1) return g.phase === "shooting" && myTurn() ? "unknown shootable" : "unknown";
        if (v === 0) return "miss";
        return "hit" + (sunkTheirs.includes(v) ? " sunk" : "");
      }, (cell) => {
        if (g.phase === "shooting" && myTurn() && g.reveals[oppSide][cell] === -1) {
          send({ type: "bs.shot", cell });
        }
      });

      // Your fleet (or P1's board for spectators): own ships + their shots.
      grid($("bs-own"), (cell) => {
        const shot = g.reveals[ownSide][cell];
        const ship = side >= 0 ? g.myFleet[cell] : Math.max(0, shot);
        let cls = ship > 0 ? "ship" : "unknown";
        if (shot === 0) cls = "miss";
        if (shot > 0) cls = "hit ship";
        return cls;
      }, null);

      // Sunk summary.
      const mineSunk = ((g.sunk && g.sunk[ownSide]) || []).map((id) => SHIPS.find((s) => s.id === id).name);
      const theirsSunk = sunkTheirs.map((id) => SHIPS.find((s) => s.id === id).name);
      $("bs-sunk").textContent =
        (theirsSunk.length ? `You sank: ${theirsSunk.join(", ")}. ` : "") +
        (mineSunk.length ? `Lost: ${mineSunk.join(", ")}.` : "");
    }

    function renderPlacement() {
      const shipsEl = $("bs-ships");
      shipsEl.innerHTML = "";
      const placed = placedIds();
      for (const ship of SHIPS) {
        const b = document.createElement("button");
        b.className = "start-btn" + (placing === ship.id ? " active" : "");
        b.textContent = `${ship.name} (${ship.len})` + (placed.has(ship.id) ? " ✓" : "");
        b.addEventListener("click", () => {
          placing = ship.id;
          origin = null;
          render();
        });
        shipsEl.appendChild(b);
      }
      grid($("bs-place-grid"), (cell) => {
        if (origin === cell) return "origin";
        return fleet[cell] ? "ship" : "unknown placeable";
      }, onPlaceCell);
      $("bs-confirm").disabled = placed.size !== 5;
    }

    $("bs-randomize").addEventListener("click", () => {
      randomizeFleet();
      render();
    });
    $("bs-confirm").addEventListener("click", () => {
      send({ type: "bs.commit", fleet });
    });
    $("bs-resign").addEventListener("click", () => {
      if (confirm("Resign battleship?")) send({ type: "bs.resign" });
    });

    return {
      onEvent(type, e) {
        if (type !== "battleship.state") return;
        g = e;
        render();
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (g.phase === "over") return { status: "over", detail: g.outcome };
        const needsMe = (g.phase === "placing" && isPlayer() && !g.committed[mySide()]) ||
          (g.phase === "shooting" && myTurn());
        return { status: "live", myTurn: needsMe };
      },
    };
  }
})();
