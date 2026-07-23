// bgboard.js — pure-DOM backgammon board rendered from the shared position.
// Zero rules: the WASM core supplies legal turns; this file draws points,
// stacks checkers, and reports clicks.
//
// Layout (white's global numbering, the shared board orientation):
//
//   13 14 15 16 17 18 | bar | 19 20 21 22 23 24     off trays →
//   12 11 10  9  8  7 | bar |  6  5  4  3  2  1
//
// Exposes window.BGBoard = { render, onClick }.
(() => {
  "use strict";

  let clickHandler = null;

  // st: {points[25], barW, barB, offW, offB}; hi: {sources:Set, targets:Set}
  // where entries are global point numbers, 25 = bar, 0 = off.
  function render(el, st, hi) {
    el.innerHTML = "";
    const top = [13, 14, 15, 16, 17, 18, -1, 19, 20, 21, 22, 23, 24];
    const bottom = [12, 11, 10, 9, 8, 7, -1, 6, 5, 4, 3, 2, 1];

    el.appendChild(row(top, true, st, hi));
    el.appendChild(row(bottom, false, st, hi));
    el.appendChild(trays(st, hi));
  }

  function row(pointNums, isTop, st, hi) {
    const r = document.createElement("div");
    r.className = "bg-row" + (isTop ? " top" : " bottom");
    for (const p of pointNums) {
      r.appendChild(p === -1 ? barCell(isTop, st, hi) : pointCell(p, isTop, st, hi));
    }
    return r;
  }

  function pointCell(p, isTop, st, hi) {
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "bg-point " + (p % 2 === (isTop ? 0 : 1) ? "odd" : "even") + (isTop ? " top" : " bottom");
    cell.dataset.point = String(p);
    if (hi && hi.sources.has(p)) cell.classList.add("source");
    if (hi && hi.targets.has(p)) cell.classList.add("target");

    const n = st.points[p]; // + white, - black
    const count = Math.abs(n);
    const color = n > 0 ? "w" : "b";
    for (let i = 0; i < Math.min(count, 5); i++) {
      const c = document.createElement("span");
      c.className = "checker " + color;
      cell.appendChild(c);
    }
    if (count > 5) {
      const more = document.createElement("span");
      more.className = "count";
      more.textContent = String(count);
      cell.appendChild(more);
    }
    const label = document.createElement("span");
    label.className = "pt-label";
    label.textContent = String(p);
    cell.appendChild(label);

    cell.addEventListener("click", () => clickHandler && clickHandler(p));
    return cell;
  }

  function barCell(isTop, st, hi) {
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "bg-bar";
    cell.dataset.point = "25";
    if (hi && hi.sources.has(25)) cell.classList.add("source");
    // Top half shows black's bar checkers, bottom white's (near their
    // re-entry direction).
    const n = isTop ? st.barB : st.barW;
    const color = isTop ? "b" : "w";
    for (let i = 0; i < Math.min(n, 4); i++) {
      const c = document.createElement("span");
      c.className = "checker " + color;
      cell.appendChild(c);
    }
    if (n > 4) {
      const more = document.createElement("span");
      more.className = "count";
      more.textContent = String(n);
      cell.appendChild(more);
    }
    cell.addEventListener("click", () => clickHandler && clickHandler(25));
    return cell;
  }

  function trays(st, hi) {
    const t = document.createElement("div");
    t.className = "bg-trays";
    for (const [who, n] of [["b", st.offB], ["w", st.offW]]) {
      const tray = document.createElement("button");
      tray.type = "button";
      tray.className = "bg-tray " + who;
      tray.dataset.point = "0";
      // Only the mover's own tray is ever a bear-off target.
      if (hi && hi.targets.has(0) && hi.mover === who) tray.classList.add("target");
      tray.textContent = `${who === "w" ? "⚪" : "⚫"} off: ${n}`;
      tray.addEventListener("click", () => clickHandler && clickHandler(0));
      t.appendChild(tray);
    }
    return t;
  }

  window.BGBoard = {
    render,
    onClick(fn) { clickHandler = fn; },
  };
})();
