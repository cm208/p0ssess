// The script for the pages beside the home page: pricing, business, welcome and the policies.
// It sets the links release.js names, draws the hairlines, chart marks and bars as they scroll
// into view (the same rule as page.js), shows what a chart's mark stands for, and on the
// pricing page reads what is for sale, runs the sign-up form and opens checkout. Every page is
// complete with scripts off: the prices are in the markup and the buy buttons are a link that
// opens an email to us ("Opening soon, get notified").
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
  const https = (u) => { try { const url = new URL(u); return url.protocol === "https:" ? url.href : ""; } catch { return ""; } };
  const cfg = rel.offer || {};
  const portal = https(cfg.portal);
  if (docs) $$('[data-link="docs"]').forEach((a) => link(a, docs));
  if (repo) $$('[data-link="repo"]').forEach((a) => link(a, repo));
  if (portal) $$('[data-link="portal"]').forEach((a) => link(a, portal));

  // Hairlines draw out of their violet start, a chart's marks land and the bars of a
  // side-by-side grow from the baseline, once each. Bars move at one speed, so the shorter
  // one lands first.
  const grow = (table) => {
    for (const bar of $$("rect.b", table)) bar.style.transitionDuration = 240 + parseFloat(bar.getAttribute("width")) * 9 + "ms";
    table.classList.add("in");
  };
  const lines = $$(".hl, .sec > .wrap, .land, .duel-t");
  if ("IntersectionObserver" in window) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        if (e.target.classList.contains("duel-t")) grow(e.target); else e.target.classList.add("in");
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.02 });
    lines.forEach((el) => seen.observe(el));
  } else {
    lines.forEach((el) => el.classList.add("in"));
  }

  // What a mark stands for, shown beside it while the pointer is on it. The same figures are in
  // the table under the chart, so nothing is readable only here.
  let tip = null, tipBox = null;
  const hideTip = () => { if (tip) tip.classList.remove("on"); };
  doc.addEventListener("pointerover", (e) => {
    const hit = e.target.closest && e.target.closest(".hit[data-tip]");
    if (!hit) return hideTip();
    const box = hit.closest(".plot-box") || hit.closest("li") || hit.parentElement.parentElement;
    if (box !== tipBox) {
      if (tip) tip.remove();
      tip = doc.createElement("span");
      tip.className = "tip";
      tip.setAttribute("aria-hidden", "true");
      if (getComputedStyle(box).position === "static") box.style.position = "relative";
      box.append(tip);
      tipBox = box;
    }
    tip.textContent = hit.dataset.tip;
    const b = box.getBoundingClientRect(), h = hit.getBoundingClientRect();
    const x = h.left + h.width / 2 - b.left, w = tip.offsetWidth;
    tip.style.left = Math.max(0, Math.min(b.width - w, x - w / 2)) + "px";
    tip.style.top = h.top - b.top - tip.offsetHeight - 4 + "px";
    tip.classList.add("on");
  });
  doc.addEventListener("pointerdown", (e) => { if (!(e.target.closest && e.target.closest(".hit[data-tip]"))) hideTip(); });

  const api = String(cfg.api || "").replace(/\/+$/, "");

  // Welcome, where Polar sends a buyer after checkout. The page ships as the plain case: sign in,
  // then link the install. With a checkout id in the address, this asks the cloud API whether that
  // checkout was paid and says so, and points the sign-in buttons at the page that ties the
  // checkout to whoever signs in.
  if ($("[data-next]")) {
    // The command's copy button, as on the home page.
    const copy = $(".copy-btn"), status = $("#copy-status");
    if (copy) {
      let timer = 0;
      copy.hidden = false;
      copy.addEventListener("click", async () => {
        const code = $(".cmd code");
        let copied = true;
        try {
          await navigator.clipboard.writeText(code.textContent);
        } catch {
          copied = false;
          const range = doc.createRange();
          range.selectNodeContents(code);
          const sel = getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
        }
        copy.textContent = copied ? "Copied" : "Selected";
        status.textContent = copied ? "Copied." : "Selected. Copy it with your keyboard.";
        clearTimeout(timer);
        timer = setTimeout(() => { copy.textContent = "Copy"; status.textContent = ""; }, 1800);
      });
    }

    // Polar adds a token for its portal to the address it sends the buyer to. This page has no use
    // for it, so it is taken out of the address bar and the history.
    const q = new URLSearchParams(location.search);
    const id = q.get("checkout_id") || "";
    if (q.has("customer_session_token")) {
      q.delete("customer_session_token");
      history.replaceState(null, "", location.pathname + (q.toString() ? "?" + q : ""));
    }
    if (api && id) {
      // The pricing page kept a claim for the checkout it started in this tab. With it, the checkout
      // belongs to whoever signs in here; without it, the address the buyer paid with is what matches.
      let claim = "";
      try {
        const kept = JSON.parse(sessionStorage.getItem("p0ssess.checkout")) || {};
        if (kept.id === id && typeof kept.claim === "string") claim = kept.claim;
      } catch { /* no claim */ }
      const done = new URLSearchParams({ checkout_id: id });
      if (claim) done.set("claim", claim);
      const email = $("[data-email]"), github = $("[data-github]"), plain = $("[data-signin]");
      email.href = `${api}/pro/done?${done}`;
      done.set("with", "github");
      github.href = `${api}/pro/done?${done}`;
      email.hidden = github.hidden = false;
      plain.hidden = true;
      $("[data-hint]").hidden = !!claim;

      const h = $("[data-h]"), lede = $("[data-lede]"), paid = $("[data-paid]"), next = $("[data-next]");
      const acts = $("[data-acts]"), again = $("[data-again]"), other = $("[data-signin-alt]"), back = $("[data-back]");
      const show = (title, text, { steps = false, line = "", retry = false, signin = false, pricing = false } = {}) => {
        h.textContent = title;
        lede.textContent = text;
        paid.textContent = line;
        paid.hidden = !line;
        next.hidden = !steps;
        again.hidden = !retry;
        other.hidden = !signin;
        back.hidden = !pricing;
        acts.hidden = !(retry || signin || pricing);
        doc.title = `${title.replace(/\.$/, "")} · p0ssess`;
      };
      const names = { pro: "Pro", team: "Team" }, periods = { year: "yearly", month: "monthly" };

      const TRIES = 8;   // while Polar is still confirming: one look every 2.5 seconds
      let tries = 0;
      const look = async () => {
        const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 8000);
        let code = 0, co = {};
        try {
          const res = await fetch(`${api}/v1/checkout?id=${encodeURIComponent(id)}`, { credentials: "omit", cache: "no-store", signal: stop.signal });
          code = res.status;
          if (res.ok) co = (await res.json()) || {};
        } catch { /* no answer: the page stays as it shipped, and signing in still checks the payment */ }
        clearTimeout(timer);
        if (code === 404) {
          return show("Nothing to finish here.", "Polar sends you to this page after a payment, and this link doesn’t match one. If you did pay, sign in with the address you paid with and Pro will be on your account.", { signin: true, pricing: true });
        }
        if (code !== 200) return;
        const name = names[co.tier] || "Pro";
        if (co.status === "paid") {
          show(`You have ${name}.`, "Polar took the payment and emails a confirmation to the address you gave it. Two things are left: sign in, then link your install.",
            { steps: true, line: `${name}${periods[co.interval] ? ", " + periods[co.interval] : ""}. Paid through Polar.` });
          const mark = $(".lockup .g");
          if (mark && root.classList.contains("anim")) mark.classList.add("blink");
        } else if (co.status === "pending") {
          if (++tries < TRIES) {
            show("Polar is confirming the payment.", "This page looks again every few seconds. If your bank asked you to approve the payment, finish that first.");
            setTimeout(look, 2500);
          } else {
            show("Polar hasn’t confirmed the payment yet.", "If your bank asked you to approve it, finish that, then check again.", { retry: true });
          }
        } else {
          show("This checkout wasn’t paid.", "Nothing was charged. The plans are on the pricing page when you want them.", { pricing: true });
        }
      };
      again.addEventListener("click", () => { tries = 0; look(); });
      look();
    }
    return;
  }

  // Pricing. The prices are in the markup. release.js names the cloud API (offer.api) and may
  // carry prices of its own (offer.prices, in US cents) for when the API cannot be reached.
  // With an API, the page asks it what is for sale and shows what it says. A price the API marks
  // as not decided is never shown as a number: the page says the price is being set.
  const tiers = $(".tiers-sec");
  if (!tiers) return;
  const cents =(v) => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : null);
  const fallback = cfg.prices || {};
  // For each price: cents to show, null to leave the markup's price, false when not decided.
  const state = {
    price: { pro_year: cents(fallback.pro_year), pro_month: cents(fallback.pro_month), team_seat_month: cents(fallback.team_seat_month) },
    hours: {},      // sandbox hours a month, by Pro's billing period
    start: "",      // where Pro's checkout begins without this page's script, once it is open
    team: "",       // where a team is started, once checkout is open
    live: false,    // the cloud API answered with an offer, so the address form works
  };

  const slots = $$("[data-price]", tiers).map((el) => ({ el, key: el.dataset.price, amount: $("[data-amount]", el), each: $("[data-each]", el),
    unset: $(".price-unset", el.parentElement) }));
  const money = (c) => (c % 100 ? (c / 100).toFixed(2) : String(c / 100));
  // What a year of Pro costs less when it is paid at once. It is said under both prices, and only
  // when both are decided and yearly is the cheaper one.
  const saveEl = $("[data-save]", tiers), overEl = $("[data-over]", tiers), swapEl = $("[data-swap]", tiers);
  const paintSaving = () => {
    const y = state.price.pro_year, m = state.price.pro_month;
    if (y === null && m === null) return;   // the markup's own prices
    const both = typeof y === "number" && typeof m === "number";
    const save = both ? m * 12 - y : 0;
    saveEl.hidden = swapEl.hidden = !(save > 0);
    overEl.textContent = both ? `billed monthly, $${money(m * 12)} over a year` : "billed monthly";
    if (save > 0) {
      saveEl.textContent = `$${money(save)} a year less than paying monthly`;
      $("[data-save-alt]", swapEl).textContent = `Yearly is $${money(save)} a year less.`;
    }
  };
  const paintPrices = () => {
    const groups = new Map();
    for (const s of slots) {
      const c = state.price[s.key];
      s.el.hidden = c === false;
      if (typeof c === "number") {
        s.amount.textContent = money(c);
        if (s.each) s.each.textContent = `$${money(Math.round(c / 12))} a month, billed once a year`;
      }
      if (s.unset) groups.set(s.unset, (groups.get(s.unset) ?? true) && c === false);
    }
    // The line that says a price is being set shows only when every price beside it is unset.
    for (const [el, all] of groups) el.hidden = !all;
    paintSaving();
  };

  // The sandbox allowance, the buy button and its terms follow the billing period in view.
  const year = $("#bill-year", tiers), month = $("#bill-month", tiers);
  const hoursEl = $("[data-hours]", tiers), renewsEl = $("[data-renews]", tiers);
  const proStart = $('[data-buy="pro"] [data-start]', tiers);
  const paintPeriod = () => {
    const h = state.hours[month.checked ? "month" : "year"];
    hoursEl.textContent = h ? `The allowance is ${h} hours a month.` : "";
    proStart.textContent = month.checked ? "Get Pro monthly" : "Get Pro yearly";
    renewsEl.textContent = month.checked ? "It renews each month until you cancel." : "It renews each year until you cancel.";
  };
  let chose = false;
  for (const r of [year, month]) r.addEventListener("change", () => { chose = true; paintPeriod(); });

  // The buttons. The page ships with a link that opens an email to us, and keeps it for as long as
  // there is no API, or the API cannot be reached: nothing can be sent from the page then. Once the
  // API has answered with an offer, Pro and Team take an address on the page. Once checkout is
  // open, Pro opens it here and Team links to where a team is made.
  const boxes = $$("[data-buy]", tiers).map((box) => ({
    box, plan: box.dataset.buy, mail: $("[data-mail]", box), open: $("[data-open]", box), start: $("[data-start]", box),
    form: $("form", box), input: $("input", box), send: $(".wl-send", box), say: $(".buy-say", box), note: $("[data-note]", box),
    terms: $("[data-terms]", box), done: false, busy: false,
  }));
  const paintBuy = () => {
    for (const b of boxes) {
      const buy = b.plan === "team" ? state.team : state.start;
      const mail = !state.live && !buy;
      b.mail.hidden = !mail;
      b.note.hidden = !mail;
      b.start.hidden = !buy;
      b.terms.hidden = !buy;
      if (buy) b.start.href = buy;
      if (buy || b.done) b.form.hidden = true;
      b.open.hidden = !state.live || !!buy || b.done || !b.form.hidden;
      if (buy && b.done) { b.done = false; b.box.classList.remove("done"); b.say.textContent = ""; }
    }
    // The portal is for people who have bought, so its link waits for checkout.
    const way = $("[data-portal]");
    if (way) way.hidden = !(portal && state.start);
  };
  const paint = () => { paintPrices(); paintPeriod(); paintBuy(); };

  const said = {
    ok: (name) => `You’re on the list. We’ll send one email when ${name} opens, then delete your address.`,
    bad: "That doesn’t look like an email address.",
    busy: "Too many tries. Try again in a while.",
    down: "Couldn’t reach the server. Try again later.",
    shut: "Checkout isn’t open yet.",
    noco: "Couldn’t open checkout, and nothing was charged. Try again in a minute.",
    closed: "Checkout closed. Nothing was charged.",
    left: "Checkout closed.",
  };
  // The line is emptied first, so the same message twice in a row is still announced.
  const tell = (b, text) => { b.say.textContent = ""; setTimeout(() => { b.say.textContent = text; }, 50); };
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
      const refuse = (text, field) => {
        tell(b, text);
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

  // Checkout. "Get Pro" asks the cloud API for a checkout of the period in view, and Polar's own
  // script (a pinned copy in vendor/) shows it in a frame over this page. The layer #pay sits
  // under that frame: the ground, the mark, and the hand-off while the frame loads. If the frame
  // cannot be used, or does not load in time, the same checkout opens as Polar's own page. With
  // scripts off, or if the API gives no checkout, the button is a link to where the API says
  // checkout starts.
  const pro = boxes.find((b) => b.plan === "pro");
  const pay = $("#pay");
  const EMBED = "../vendor/polar-checkout-embed-0.6.0.js";
  // How long the frame gets before Polar's own page is used instead: this long in all, and this
  // long after the frame's document has loaded without the checkout in it saying so.
  const WAIT = 12000, GRACE = 5000;
  const framed = cfg.embed === true;
  // What this tab needs to finish a checkout it started: its id, and the claim /pro/done asks for.
  const KEY = "p0ssess.checkout";
  const keep = (v) => { try { sessionStorage.setItem(KEY, JSON.stringify(v)); } catch { /* the address the buyer paid with still matches */ } };
  const kept = () => { try { return JSON.parse(sessionStorage.getItem(KEY)) || {}; } catch { return {}; } };
  let embed = null;
  const loadEmbed = () => embed || (embed = new Promise((ok, no) => {
    const tag = doc.createElement("script");
    tag.src = EMBED;
    tag.onload = () => (window.Polar && window.Polar.EmbedCheckout ? ok(window.Polar.EmbedCheckout) : no());
    tag.onerror = no;
    doc.head.append(tag);
  }).catch(() => null));
  const frames = () => $$('body > iframe[src*="embed=true"]');
  const behind = () => $$("body > .skip, body > header, body > main, body > footer");
  // One try at a checkout. `run` goes up whenever the layer comes down or the page is left, so a
  // step that was waiting on the network can tell it is stale.
  const cur = { on: false, run: 0, id: "", paying: false, checkout: null, timer: 0 };
  const showPay = (title, sub) => {
    $("[data-pay-say]", pay).textContent = title;
    $("[data-pay-sub]", pay).textContent = sub;
    if (cur.on) return;
    cur.on = true;
    pay.hidden = false;
    for (const el of behind()) el.inert = true;
    pay.focus();
    requestAnimationFrame(() => pay.classList.add("go"));
  };
  const dropFrame = () => {
    clearTimeout(cur.timer);
    if (cur.checkout) cur.checkout.close();
    for (const f of frames()) f.remove();
    for (const el of $$("body > div > .polar-loader-spinner")) el.parentElement.remove();
    doc.body.classList.remove("polar-no-scroll");
    cur.checkout = null;
  };
  const hidePay = () => {
    dropFrame();
    for (const el of behind()) el.inert = false;
    pay.classList.remove("go", "open");
    pay.hidden = true;
    Object.assign(cur, { on: false, run: cur.run + 1, id: "", paying: false });
    pro.busy = false;
  };
  const checkState = async (id) => {
    if (!id) return "";
    const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 6000);
    try {
      const res = await fetch(api + "/v1/checkout?id=" + encodeURIComponent(id), { credentials: "omit", cache: "no-store", signal: stop.signal });
      return res.ok ? String((await res.json()).status || "") : "";
    } catch { return ""; } finally { clearTimeout(timer); }
  };
  const welcome = (id) => new URL("../welcome/?checkout_id=" + encodeURIComponent(id), location.href).href;
  // The buyer closed the checkout, or came back from Polar's page with its back button. The API
  // says whether the checkout was paid after all; "nothing was charged" is only said when it was not.
  const closed = async (id) => {
    hidePay();
    pro.start.focus({ preventScroll: true });
    const st = await checkState(id);
    if (st === "paid" || st === "pending") return location.assign(welcome(id));
    tell(pro, st ? said.closed : said.left);
  };
  // Polar's own page, for when the frame is not used.
  const leave = (url, sub, title = "Taking you to Polar") => {
    cur.run++;
    dropFrame();
    showPay(title, sub);
    location.assign(url);
  };
  const begin = async () => {
    if (pro.busy) return;
    pro.busy = true;
    pro.say.textContent = "";
    showPay("Opening checkout", "Polar is the merchant of record. It takes the payment and works out the tax.");
    const run = cur.run;
    const Embed = framed ? await loadEmbed() : null;
    const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 10000);
    let status = 0, co = null;
    try {
      const res = await fetch(api + "/v1/checkout", {
        method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({ plan: month.checked ? "pro_monthly" : "pro_yearly", embed: Embed ? "1" : "0" }).toString(),
        credentials: "omit", cache: "no-store", signal: stop.signal,
      });
      status = res.status;
      if (res.ok) co = await res.json();
    } catch { /* no answer: reported below */ }
    clearTimeout(timer);
    if (run !== cur.run) return;   // closed while the API was asked
    const url = co && typeof co.id === "string" ? https(co.url) : "";
    if (!url) {
      // No answer the page can read, moments after the same API answered with the offer: most
      // likely a server that does not have this call yet. Its own page starts a checkout too.
      if (!status && state.start) return leave(state.start, "It continues on our account page, and then on Polar’s.", "Taking you to checkout");
      hidePay();
      if (status === 409) { state.start = state.team = ""; paintBuy(); }
      return tell(pro, status === 409 ? said.shut : status === 429 ? said.busy : said.noco);
    }
    const id = cur.id = co.id;
    keep({ id, claim: String(co.claim || "") });
    if (!Embed || co.embed !== true) return leave(url, "The checkout is on Polar’s own page. You come back here when you’re done.");
    const giveUp = () => leave(url, "The checkout didn’t open on this page, so it opens on Polar’s. You come back here when you’re done.");
    cur.timer = setTimeout(giveUp, WAIT);
    const opening = Embed.create(url, { theme: "dark" });
    for (const f of frames()) {
      f.title = "Polar checkout";
      // A frame the browser refuses, or one that fails, still loads a document: an error page.
      f.addEventListener("load", () => {
        if (run !== cur.run || cur.checkout) return;
        clearTimeout(cur.timer);
        cur.timer = setTimeout(giveUp, GRACE);
      }, { once: true });
    }
    const checkout = await opening;
    if (run !== cur.run) return checkout.close();
    clearTimeout(cur.timer);
    cur.checkout = checkout;
    pay.classList.add("open");
    for (const f of frames()) { f.classList.add("in"); f.focus(); }
    checkout.addEventListener("confirmed", () => { cur.paying = true; });
    // Polar's script keeps the frame up once a payment has started. If that payment then fails,
    // the buyer can still leave: the API says where the checkout stands.
    checkout.addEventListener("close", async () => {
      if (!cur.paying) return closed(id);
      const st = await checkState(id);
      if (run !== cur.run) return;
      if (st === "paid") location.assign(welcome(id));
      else if (st && st !== "pending") closed(id);
    });
    // Polar's script would follow the address it is given. This page goes to its own welcome page,
    // and leaves out the portal token Polar adds, so the token never reaches a server log.
    checkout.addEventListener("success", (e) => {
      e.preventDefault();
      let next = welcome(id);
      try {
        const to = new URL(e.detail.successURL);
        to.searchParams.delete("customer_session_token");
        if (to.origin === location.origin) next = to.href;
      } catch { /* the welcome page, by the checkout's id */ }
      location.assign(next);
    });
  };
  pro.start.addEventListener("click", (e) => {
    if (!api || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button) return;
    e.preventDefault();
    begin();
  });
  doc.addEventListener("keydown", (e) => { if (e.key === "Escape" && cur.on && !cur.paying) closed(cur.id); });
  // Back from Polar's page through the browser's history: the page is as it was left, with the layer up.
  addEventListener("pageshow", (e) => { if (e.persisted && cur.on) closed(kept().id); });

  // What the API says is for sale. Anything it does not say clearly counts as not decided.
  const read = (offer) => {
    const price = { pro_year: false, pro_month: false, team_seat_month: false }, hours = {};
    let onSale = false, teamOnSale = false, pick = "";
    for (const p of Array.isArray(offer.plans) ? offer.plans : []) {
      const key = p.tier === "team" ? `team_seat_${p.interval}` : `${p.tier}_${p.interval}`;
      if (!(key in price)) continue;
      const set = p.placeholder === false, cost = p.price || {};
      if (set && cost.currency === "usd") price[key] = cents(cost.amount_cents) ?? false;
      if (p.tier === "team" && p.available === true) teamOnSale = true;
      if (p.tier !== "pro") continue;
      if (set && p.sandbox_hours_per_month > 0) hours[p.interval] = Number(p.sandbox_hours_per_month);
      if (p.available === true) onSale = true;
      if (p.recommended === true) pick = p.interval;
    }
    state.price = price;
    state.hours = hours;
    const open = offer.checkout === "open";
    state.start = open && onSale ? https(offer.start_url) : "";
    state.team = open && teamOnSale ? https(offer.team_url) : "";
    // Checkout steers toward the recommended period, unless the reader already chose one.
    if (pick && !chose) {
      (pick === "month" ? month : year).checked = true;
      $("[data-rec]", tiers).textContent = pick === "month" ? "Monthly is recommended." : "Yearly is recommended.";
    }
  };

  paint();
  if (!api) return;
  // Polar's back button returns to this page with ?checkout=back. The address is tidied either way.
  const back = new URLSearchParams(location.search).get("checkout") === "back";
  if (back) history.replaceState(null, "", location.pathname + location.hash);
  const stop = new AbortController(), timer = setTimeout(() => stop.abort(), 4000);
  fetch(api + "/v1/pro/offer", { credentials: "omit", cache: "no-store", signal: stop.signal })
    .then((res) => (res.ok ? res.json() : Promise.reject(new Error(String(res.status)))))
    .then((offer) => {
      read(offer || {});
      state.live = true;
      paint();
      if (state.start && framed) loadEmbed();
      if (back && state.start) closed(kept().id);
    })
    .catch(() => { /* keeps the prices in the markup and the email link */ })
    .finally(() => clearTimeout(timer));
})();
