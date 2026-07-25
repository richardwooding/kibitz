// ginboard.js — the Gin Rummy module: draw a card (stock or upcard), then
// discard; knock when your deadwood is low enough. All rules/scoring/crypto
// live in the core; this file draws the table and forwards intents. Hidden
// information (each hand) is only ever revealed to its owner, and both hands
// are shown at showdown/over so a verified shuffle can be trusted.
(() => {
  "use strict";

  const RANKS = ["A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"];
  const SUITS = ["♠", "♥", "♦", "♣"]; // ♠ ♥ ♦ ♣

  window.GameModules = window.GameModules || {};
  window.GameModules.gin = { label: "🃏 Gin Rummy", paneId: "game-gin", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;
    let selected = -1; // card int the player has picked to discard/knock, or -1

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const mySeat = () => (g.p1Id === ctx.self() ? 0 : (g.p2Id === ctx.self() ? 1 : -1));
    const isPlayer = () => g && mySeat() >= 0;
    const myTurn = () => g && isPlayer() && g.turnId === ctx.self();
    const over = () => g && g.phase === "over";
    const oppName = () => ctx.name(mySeat() === 0 ? g.p2Id : g.p1Id);

    // ---- card + DOM helpers -------------------------------------------------

    function cardLabel(c) { return RANKS[c % 13] + SUITS[Math.floor(c / 13)]; }

    function cardNode(c, opts) {
      const o = opts || {};
      const el = document.createElement(o.onClick ? "button" : "div");
      el.className = "gin-card";
      const suit = Math.floor(c / 13);
      el.classList.add(suit === 1 || suit === 2 ? "red" : "black");
      if (o.selected) el.classList.add("selected");
      const r = document.createElement("span");
      r.className = "gin-rank"; r.textContent = RANKS[c % 13];
      const s = document.createElement("span");
      s.className = "gin-suit"; s.textContent = SUITS[suit];
      el.append(r, s);
      if (o.onClick) { el.type = "button"; el.addEventListener("click", o.onClick); }
      return el;
    }

    function cardBack() {
      const el = document.createElement("div");
      el.className = "gin-card back";
      return el;
    }

    function div(cls) {
      const d = document.createElement("div");
      if (cls) d.className = cls;
      return d;
    }

    function note(text) { const d = div("gin-note"); d.textContent = text; return d; }
    function label(text) { const d = div("gin-pilelabel"); d.textContent = text; return d; }

    function button(text, onClick) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "gin-btn";
      b.textContent = text;
      b.addEventListener("click", onClick);
      return b;
    }

    // ---- turn/action predicates --------------------------------------------

    const canDraw = () => myTurn() && g.phase === "draw" && g.stockCount > 0;
    const canTake = () => myTurn() && g.phase === "draw" && g.discard.length > 0;
    const canDiscard = () => myTurn() && g.phase === "discard";

    // ---- section builders ---------------------------------------------------

    function statusText() {
      if (over()) return g.outcome || "Game over";
      if (g.phase === "shuffling") return "Shuffling the deck…";
      if (g.phase === "showdown") return "Showdown…";
      if (!isPlayer()) return "Kibitzing…";
      if (myTurn() && g.phase === "draw") return "Your turn — draw a card";
      if (myTurn() && g.phase === "discard") return `Your turn — discard (deadwood ${g.deadwood})`;
      return "Waiting for opponent…";
    }

    function renderScores() {
      const el = $("gin-scores");
      if (el) el.textContent = `${ctx.name(g.p1Id)} ${g.scores[0]} — ${g.scores[1]} ${ctx.name(g.p2Id)}`;
    }

    function oppRow() {
      const row = div("gin-opp");
      if (mySeat() < 0) {
        row.textContent = `${ctx.name(g.p1Id)}: ${g.handCounts[0]} · ${ctx.name(g.p2Id)}: ${g.handCounts[1]}`;
      } else {
        row.textContent = `${oppName()}: ${g.handCounts[1 - mySeat()]} cards`;
      }
      return row;
    }

    function stockPile() {
      const wrap = div("gin-pile");
      wrap.append(label(`Stock (${g.stockCount})`), cardBack());
      if (canDraw()) wrap.appendChild(button("Draw stock", () => send({ type: "gin.drawStock" })));
      return wrap;
    }

    function discardPile() {
      const wrap = div("gin-pile");
      wrap.appendChild(label("Discard"));
      const top = g.discard.length ? g.discard[g.discard.length - 1] : -1;
      const take = (top >= 0 && canTake()) ? () => send({ type: "gin.takeUpcard" }) : null;
      wrap.appendChild(top >= 0 ? cardNode(top, { onClick: take }) : note("(empty)"));
      if (take) wrap.appendChild(button(`Take ${cardLabel(top)}`, take));
      return wrap;
    }

    function pilesRow() {
      const row = div("gin-piles");
      row.append(stockPile(), discardPile());
      return row;
    }

    function handSection(text, cards, selectable) {
      const wrap = div("gin-hand");
      wrap.appendChild(label(text));
      const row = div("gin-cards");
      const list = [...(cards || [])].sort((a, b) => a - b);
      for (const c of list) {
        row.appendChild(cardNode(c, {
          selected: selectable && c === selected,
          onClick: selectable ? () => selectCard(c) : null,
        }));
      }
      if (!list.length) row.appendChild(note("(hidden)"));
      wrap.appendChild(row);
      return wrap;
    }

    function controlsRow() {
      const row = div("gin-controls");
      if (!canDiscard()) return row;
      row.appendChild(note(`Deadwood: ${g.deadwood}`));
      const disc = button("Discard", doDiscard);
      disc.disabled = selected < 0;
      row.appendChild(disc);
      if (g.canKnock) {
        const k = button("Knock", doKnock);
        k.disabled = selected < 0;
        row.appendChild(k);
      }
      return row;
    }

    function verifiedBadge() {
      const b = div("gin-verify " + (g.verified ? "ok" : "muted"));
      b.textContent = g.verified
        ? "✓ Provably fair — shuffle verified"
        : "Shuffle proof unavailable";
      return b;
    }

    // ---- actions ------------------------------------------------------------

    function selectCard(c) { selected = (selected === c ? -1 : c); render(); }

    function doDiscard() {
      if (selected < 0) return;
      send({ type: "gin.discard", ginCard: selected });
      selected = -1;
    }

    function doKnock() {
      if (selected < 0) return;
      send({ type: "gin.knock", ginCard: selected });
      selected = -1;
    }

    // ---- render -------------------------------------------------------------

    function render() {
      if (!visible || !g) return;
      const statusEl = $("gin-status");
      if (statusEl) statusEl.textContent = statusText();
      renderScores();
      const resign = $("gin-resign");
      if (resign) resign.classList.toggle("hidden", !isPlayer() || !g.playing || over());
      const board = $("gin-board");
      if (!board) return;
      board.innerHTML = "";
      renderBody(board);
    }

    function renderBody(board) {
      if (!g.playing) { board.appendChild(note("Waiting for the game to start…")); return; }
      if (over()) { renderOver(board); return; }
      if (g.phase === "shuffling") { board.appendChild(note("Shuffling the deck…")); return; }
      if (g.phase === "showdown") { board.appendChild(note("Showdown…")); return; }
      board.append(oppRow(), pilesRow(), handSection("Your hand", g.hand, canDiscard()), controlsRow());
    }

    function renderOver(board) {
      board.append(
        handSection("Your hand", g.hand, false),
        handSection(`${oppName()}'s hand`, g.oppHand, false),
        verifiedBadge(),
      );
    }

    // ---- fx -----------------------------------------------------------------

    function won() {
      if (!isPlayer()) return null;
      const o = (g.outcome || "").toLowerCase();
      if (o.includes("player 1")) return mySeat() === 0;
      if (o.includes("player 2")) return mySeat() === 1;
      return null;
    }

    function maybeSound(prev) {
      if (!prev || !window.fx || !window.fx.sound || !window.fx.sound.drop) return;
      if ((g.discard || []).length !== (prev.discard || []).length) window.fx.sound.drop();
    }

    function maybeCelebrate(prev) {
      if (!prev || !prev.playing || prev.phase === "over" || !over() || !window.fx) return;
      window.fx.celebrate($("game-gin"), won(),
        window.fx.result(won(), { spectator: g.outcome, hotseat: isHotseat() }));
    }

    // ---- controls wiring ----------------------------------------------------

    const resignBtn = $("gin-resign");
    if (resignBtn) {
      resignBtn.addEventListener("click", () => {
        if (confirm("Resign Gin Rummy?")) send({ type: "gin.resign" });
      });
    }

    return {
      onEvent(type, e) {
        if (type !== "gin.state") return;
        const prev = g;
        g = e;
        if (selected >= 0 && !(g.hand || []).includes(selected)) selected = -1;
        maybeSound(prev);
        render();
        maybeCelebrate(prev);
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
