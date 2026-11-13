// The script for the pages beside the home page: pricing, business and the policies.
// It sets the links release.js names, draws the hairlines and bars as they scroll into view
// (the same rule as page.js), and on the pricing page reads what is for sale and runs the
// sign-up form. Every page is complete with scripts off: prices show as placeholders and the
// buy buttons read "Opening soon".
(() => {
  const doc = document, root = doc.documentElement;
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const $ = (s, el = doc) => el.querySelector(s);
  const $$ = (s, el = doc) => [...el.querySelectorAll(s)];

  // Motion only runs under html.anim, which follows the reader's setting.
  const syncMotion = () => root.classList.toggle("anim", !reduce.matches);
  syncMotion();
  reduce.addEventListener("change", syncMotion);

  // Outside links. release.js holds the addresses; a link without one stays hidden.
  const rel = window.P0SSESS || {};
  const repo = String(rel.repo || "").replace(/\/+$/, ""), docs = String(rel.docs || "").replace(/\/+$/, "");
  const link = (a, href) => { a.href = href; a.hidden = false; };
  if (docs) $$('[data-link="docs"]').forEach((a) => link(a, docs));
  if (repo) $$('[data-link="repo"]').forEach((a) => link(a, repo));

  // Hairlines draw out of their violet start and the bars of a side-by-side grow from the
  // baseline, once each. Bars move at one speed, so the shorter one lands first.
  const grow = (table) => {
    for (const bar of $$("rect.b", table)) bar.style.transitionDuration = 240 + parseFloat(bar.getAttribute("width")) * 9 + "ms";
    table.classList.add("in");
  };
  const lines = $$(".hl, .sec > .wrap, .duel-t");
  if ("IntersectionObserver" in window) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        if (e.target.classList.contains("duel-t")) grow(e.target); else e.target.classList.add("in");
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
    lines.forEach((el) => seen.observe(el));
  } else {
    lines.forEach((el) => el.classList.add("in"));
  }

  // Pricing. release.js names the cloud API (offer.api) and the prices to show while it cannot
  // be reached (offer.prices, in US cents; null is not decided). With an API, the page asks it
  // what is for sale. A price the API marks as a placeholder is never shown as a number.
  const tiers = $(".tiers");
  if (!tiers) return;
  const cfg = rel.offer || {};
  const api = String(cfg.api || "").replace(/\/+$/, "");
  const cents = (v) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : null);
  const fallback = cfg.prices || {};
  const state = {
    price: { pro_year: cents(fallback.pro_year), pro_month: cents(fallback.pro_month), team_seat_month: cents(fallback.team_seat_month) },
    hours: {},      // sandbox hours a month, by Pro's billing period
    start: "",      // where checkout begins, once it is open
  };

  // A price slot keeps its placeholder, so it can go back to it.
  const slots = $$("[data-price]", tiers).map((el) => ({ el, key: el.dataset.price, per: $(".per", el).textContent, blank: [...el.childNodes] }));
  const money = (c) => (c % 100 ? (c / 100).toFixed(2) : String(c / 100));
  const paintPrices = () => {
    for (const s of slots) {
      const c = state.price[s.key];
      if (c === null || c === undefined) { s.el.replaceChildren(...s.blank); continue; }
      const b = doc.createElement("b"), unit = doc.createElement("small"), per = doc.createElement("span");
      unit.textContent = "$";
      b.append(unit, money(c));
      per.className = "per";
      per.textContent = s.per;
      s.el.replaceChildren(b, " ", per);
    }
  };

  // The sandbox allowance follows the billing period in view.
  const year = $("#bill-year", tiers), month = $("#bill-month", tiers);
  const hoursEl = $("[data-hours]", tiers), hoursBlank = [...hoursEl.childNodes];
  const paintHours = () => {
    const h = state.hours[month.checked ? "month" : "year"];
    if (h) hoursEl.replaceChildren(`The allowance is ${h} hours a month.`); else hoursEl.replaceChildren(...hoursBlank);
  };
  let chose = false;
  for (const r of [year, month]) r.addEventListener("change", () => { chose = true; paintHours(); });

  // The buttons. Without an API nothing can be sent, so they stay as the page ships: "Opening
  // soon", off. With one, Pro and Team take an address; once checkout is open, Pro links to it.
  const boxes = $$("[data-buy]", tiers).map((box) => ({
    box, plan: box.dataset.buy, off: $("[data-off]", box), open: $("[data-open]", box), start: $("[data-start]", box),
    form: $("form", box), input: $("input", box), send: $(".wl-send", box), say: $(".buy-say", box), note: $("[data-note]", box),
    done: false, busy: false,
  }));
  const paintBuy = () => {
    for (const b of boxes) {
      const buy = b.start && state.start;
      b.off.hidden = !!api;
      b.note.hidden = !!api;
      if (b.start) { b.start.hidden = !buy; if (buy) b.start.href = state.start; }
      if (buy || b.done) b.form.hidden = true;
      b.open.hidden = !api || !!buy || b.done || !b.form.hidden;
      if (buy && !b.done) b.say.textContent = "";
    }
  };
  const paint = () => { paintPrices(); paintHours(); paintBuy(); };

  const said = {
    ok: (name) => `You\u2019re on the list. We\u2019ll send one email when ${name} opens, then delete your address.`,
    bad: "That doesn\u2019t look like an email address.",
    busy: "Too many tries. Try again in a while.",
    down: "Couldn\u2019t reach the server. Try again later.",
  };
  for (const b of boxes) {
    b.open.addEventListener("click", () => {
      b.form.hidden = false;
      b.open.hidden = true;
      b.input.focus();
    });
    b.input.addEventListener("input", () => b.input.removeAttribute("aria-invalid"));
    b.form.addEventListener("submit", async (e) => {
      e.preventDefault();
      if (b.busy) return;
      const email = b.input.value.trim();
      // The line is emptied first, so the same message twice in a row is still announced.
      const refuse = (text, field) => {
        b.say.textContent = "";
        setTimeout(() => { b.say.textContent = text; }, 50);
        if (field) { b.input.setAttribute("aria-invalid", "true"); b.input.focus(); }
      };
      if (!email || !b.input.checkValidity()) return refuse(said.bad, true);
      b.busy = true;
      b.send.disabled = true;
      b.send.textContent = "Sending";
      b.say.textContent = "";
      // A form-encoded body keeps this a simple request, so the browser sends no preflight.
      const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 10000);
      let status = 0;
      try {
        const res = await fetch(api + "/v1/waitlist", {
          method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ email, plan: b.plan, source: "site" }).toString(),
          credentials: "omit", cache: "no-store", signal: stop.signal,
        });
        status = res.status;
      } catch { /* no answer: reported below */ }
      clearTimeout(timer);
      b.busy = false;
      b.send.disabled = false;
      b.send.textContent = "Notify me";
      if (status === 204) {
        b.done = true;
        b.box.classList.add("done");
        b.say.textContent = said.ok(b.plan === "team" ? "Team" : "Pro");
        paintBuy();
      } else if (status === 400) refuse(said.bad, true);
      else if (status === 429) refuse(said.busy);
      else refuse(said.down);
    });
  }

  // What the API says is for sale. Anything it does not say clearly counts as not decided.
  const https = (u) => { try { const url = new URL(u); return url.protocol === "https:" ? url.href : ""; } catch { return ""; } };
  const read = (offer) => {
    const price = { pro_year: null, pro_month: null, team_seat_month: null }, hours = {};
    let onSale = false, pick = "";
    for (const p of Array.isArray(offer.plans) ? offer.plans : []) {
      const key = p.tier === "team" ? `team_seat_${p.interval}` : `${p.tier}_${p.interval}`;
      if (!(key in price)) continue;
      const set = p.placeholder === false, cost = p.price || {};
      if (set && cost.currency === "usd") price[key] = cents(cost.amount_cents);
      if (p.tier !== "pro") continue;
      if (set && p.sandbox_hours_per_month > 0) hours[p.interval] = Number(p.sandbox_hours_per_month);
      if (p.available === true) onSale = true;
      if (p.recommended === true) pick = p.interval;
    }
    state.price = price;
    state.hours = hours;
    state.start = offer.checkout === "open" && onSale ? https(offer.start_url) : "";
    // Checkout steers toward the recommended period, unless the reader already chose one.
    if (pick && !chose) {
      (pick === "month" ? month : year).checked = true;
      $("[data-rec]", tiers).textContent = pick === "month" ? "Monthly is recommended." : "Yearly is recommended.";
    }
  };

  paint();
  if (!api) return;
  const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 4000);
  fetch(api + "/v1/pro/offer", { credentials: "omit", cache: "no-store", signal: stop.signal })
    .then((res) => (res.ok ? res.json() : Promise.reject(new Error(String(res.status)))))
    .then((offer) => { read(offer || {}); paint(); })
    .catch(() => { /* keeps the prices from release.js and "Opening soon" */ })
    .finally(() => clearTimeout(timer));
})();
