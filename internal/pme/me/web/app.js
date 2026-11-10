"use strict";
(() => {
  const $ = (s) => document.querySelector(s);
  const el = (tag, attrs = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === "class") n.className = v; else if (k === "text") n.textContent = v; else n.setAttribute(k, v);
    }
    for (const k of kids) if (k != null && k !== false) n.append(k);
    return n;
  };
  const list = (v) => (Array.isArray(v) ? v : []);
  const money = (v) => {
    v = Number(v) || 0;
    return v >= 100 ? "$" + Math.round(v).toLocaleString("en-US") : "$" + v.toFixed(2);
  };
  const cost = (v) => (v > 0 && v < 0.01 ? "<$0.01" : money(v));
  const pct = (v) => Math.round((v || 0) * 100) + "%";
  const short = (id) => (id || "").split("/").pop();
  const cap = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : "");
  const words = (id) => cap((id || "").replace(/-/g, " "));
  const span = (secs) => {
    secs = Math.max(0, Math.round(secs));
    if (secs < 60) return secs + " s";
    const m = Math.round(secs / 60);
    if (m < 60) return m + " min";
    const h = Math.floor(m / 60);
    if (h < 24) return h + " h";
    return Math.round(h / 24) + " d";
  };
  const ago = (iso) => {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    return s < 60 ? "just now" : span(s) + " ago";
  };
  let token = "";
  let dirty = false;
  const PRESETS = { list: "Always list price", gentle: "Gentle", balanced: "Balanced", eager: "Eager" };

  async function post(path, body) {
    const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", "X-P0ssess-Token": token }, body: JSON.stringify(body) });
    let j = {};
    try { j = await r.json(); } catch (e) { /* not JSON */ }
    if (!r.ok) throw new Error(j.error || r.statusText || "Request failed");
    return j;
  }

  function renderFigures(s) {
    const st = s.status, b = st.budget, sp = st.spend;
    $("#today").textContent = money(sp.today_usd);
    const paced = b.daily_usd === 0 ? " (paced)" : "";
    $("#today-sub").textContent = st.daily_limit_usd < 0 || b.daily_usd < 0 ? "No daily limit" : "of " + money(st.daily_limit_usd) + " today" + paced;

    const spent = sp.month_usd + (sp.reserved_usd || 0);
    const frac = b.monthly_usd > 0 ? spent / b.monthly_usd : 0;
    const month = $("#month");
    month.textContent = money(spent);
    month.parentElement.classList.toggle("near", !!st.near_budget);
    $("#month-cap").textContent = b.monthly_usd > 0 ? "of " + money(b.monthly_usd) : "no cap";
    const m = $("#month-meter");
    m.classList.toggle("near", !!st.near_budget);
    m.firstElementChild.style.width = (Math.min(1, frac) * 100).toFixed(1) + "%";
    m.setAttribute("aria-label", "Monthly spend, " + pct(frac) + " of the cap");
    const runs = sp.runs_this_month || 0;
    $("#month-sub").textContent = runs + (runs === 1 ? " run" : " runs") + " · " + money(st.month_left_usd) + " left" + (sp.reserved_usd ? " · " + money(sp.reserved_usd) + " held for running calls" : "");

    const jobs = list(st.running);
    $("#running-n").textContent = String(jobs.length);
    $("#running").textContent = jobs.length ? jobs.map((j) => short(j.model) + (j.use_case ? " · " + words(j.use_case).toLowerCase() : "")).join(", ") : "Nothing handed off right now";

    const lam = st.lambda || {};
    const wins = list(s.windows);
    const api = (lam.plan || st.plan) === "api";
    if (wins.length) {
      const worst = wins.reduce((a, w) => (w.used > a.used ? w : a));
      $("#claude").textContent = pct(worst.used);
      $("#claude-sub").textContent = wins.map((w) => w.name + " " + pct(w.used) + (w.projected > w.used + 0.005 ? ", headed for " + pct(w.projected) : "")).join(" · ") + " · λ " + (lam.value || 0).toFixed(2);
    } else {
      $("#claude").textContent = "λ " + (lam.value || 0).toFixed(2);
      $("#claude-sub").textContent = api ? "API key: Claude counts at list price" : "No usage reading yet; it comes in through the status line";
    }
  }

  function renderRuns(runs) {
    const box = $("#runs");
    runs = list(runs).slice(0, 7);
    if (!runs.length) {
      box.replaceChildren(el("li", { class: "empty", text: "Nothing handed off yet. Ask Claude to watch a video or make an image." }));
      return;
    }
    box.replaceChildren(...runs.map((r) => el("li", {},
      el("span", { class: "what", text: words(r.use_case || r.route || "run") }, r.status && r.status !== "done" ? el("span", { class: "tag", text: r.status }) : null),
      el("span", { class: "who", text: short(r.model) }),
      el("span", { class: "cost", text: cost(r.cash_usd) + (r.estimated ? "*" : "") }),
      el("span", { class: "when", text: ago(r.ts) }))));
  }

  function renderLimits(s) {
    const b = s.status.budget;
    const items = [
      ["Asks above " + money(b.per_task_usd), true],
      [b.daily_usd < 0 ? "No daily limit" : money(s.status.daily_limit_usd) + " a day" + (b.daily_usd === 0 ? ", paced" : ""), false],
      ["Cap " + money(b.monthly_usd) + " a month", false],
      [b.privacy && b.privacy.mode === "zdr" ? "Zero retention" : "No training", false],
      [(PRESETS[s.lambda_preset] || cap(s.lambda_preset || "custom")) + " curve", false],
    ];
    if (b.ask && b.ask.first_use) items.push(["Asks on first runs", true]);
    if (b.ask && b.ask.code_edits) items.push(["Asks before edits", true]);
    const net = list(b.sandbox && b.sandbox.shell_internet);
    if (net.length) items.push(["Shell internet: " + net.length + (net.length === 1 ? " project" : " projects"), false]);
    $("#limits").replaceChildren(...items.map(([t, on]) => el("li", { class: on ? "on" : null, text: t })));
  }

  function ruleUse(r) {
    if (!r.used || !(r.max_count || r.max_total_usd)) return "";
    const parts = [];
    if (r.max_count) parts.push(r.used.count + " of " + r.max_count + " runs");
    if (r.max_total_usd) parts.push(money(r.used.total_usd) + " of " + money(r.max_total_usd));
    return "used " + parts.join(", ") + (r.period === "day" ? " today" : "");
  }

  function renderRules(rs, learned) {
    rs = list(rs);
    const box = $("#rules");
    if (!rs.length) {
      box.replaceChildren(el("li", { class: "empty", text: "None. Every route is open within the limits." }));
    } else {
      box.replaceChildren(...rs.map((r) => {
        const sub = [r.scope];
        if (r.expired) sub.push("expired");
        else if (r.expires) sub.push("until " + new Date(r.expires).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }));
        const use = ruleUse(r);
        if (use) sub.push(use);
        if (r.ignored) sub.push("ignored: " + r.ignored);
        const text = cap(r.text.replace(/ \((session|project|global)[^)]*\)$/, ""));
        const rm = el("button", { class: "pill quiet", type: "button", text: "Remove", "aria-label": "Remove rule: " + text });
        rm.addEventListener("click", async () => {
          rm.disabled = true;
          try { await post("api/rules/remove", { id: r.id }); load(); } catch (e) { rm.disabled = false; rm.textContent = "Retry"; rm.title = e.message; }
        });
        return el("li", {}, el("span", { class: "text" }, text, el("small", { text: sub.join(" · ") })), r.ignored ? null : rm);
      }));
    }
    $("#suggestions").replaceChildren(...list(learned && learned.suggestions).slice(0, 2).map((sg) => {
      const b = el("button", { class: "pill quiet", type: "button", text: "Save as rule" });
      const msg = el("p", { text: cap(sg.text) });
      b.addEventListener("click", async () => {
        b.disabled = true;
        try { await post("api/rules/add", { rule: sg.rule }); load(); } catch (e) { b.disabled = false; msg.textContent = "Couldn't save: " + e.message; }
      });
      return el("div", { class: "suggest" }, msg, b);
    }));
  }

  function fillSettings(s) {
    if (dirty) return;
    const b = s.status.budget, f = $("#settings");
    const sel = $("#preset");
    const names = list(s.presets).slice();
    if (s.lambda_preset && !names.includes(s.lambda_preset)) names.push(s.lambda_preset);
    if (sel.options.length !== names.length) sel.replaceChildren(...names.map((p) => el("option", { value: p, text: PRESETS[p] || cap(p) })));
    const vals = {
      monthly_usd: b.monthly_usd, per_task_usd: b.per_task_usd, daily_usd: b.daily_usd, plan: b.plan,
      "lambda.preset": s.lambda_preset, save_claude: b.save_claude, usage_credits: b.usage_credits,
      "privacy.mode": b.privacy.mode, "privacy.allow_safety_retention": b.privacy.allow_safety_retention,
      "ask.first_use": b.ask && b.ask.first_use, "ask.code_edits": b.ask && b.ask.code_edits,
      "models.deny": list(b.models && b.models.deny).join(", "), "providers.deny": list(b.providers && b.providers.deny).join(", "),
      "statusline.command": (s.statusline && s.statusline.command) || "",
    };
    for (const [k, v] of Object.entries(vals)) {
      const input = f.elements[k];
      if (!input) continue;
      if (input.type === "checkbox") input.checked = !!v; else input.value = v == null ? "" : v;
      input.dataset.orig = input.type === "checkbox" ? String(!!v) : String(v == null ? "" : v);
    }
  }

  const saved = (text, err) => { const o = $("#saved"); o.textContent = text; o.classList.toggle("err", !!err); };
  const sheet = $("#settings-sheet");
  $("#open-settings").addEventListener("click", () => { saved(""); sheet.showModal(); });
  $("#close-settings").addEventListener("click", () => { dirty = false; sheet.close(); load(); });
  $("#settings").addEventListener("input", () => { dirty = true; saved(""); });
  $("#settings").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const changes = {};
    for (const input of ev.target.elements) {
      if (!input.name) continue;
      const v = input.type === "checkbox" ? String(input.checked) : input.value.trim();
      if (v !== input.dataset.orig) changes[input.name] = v;
    }
    if (!Object.keys(changes).length) { saved("Nothing changed."); return; }
    for (const k of ["monthly_usd", "per_task_usd", "daily_usd"]) {
      if (k in changes && (changes[k] === "" || !Number.isFinite(Number(changes[k])))) {
        saved("That limit needs a number.", true);
        ev.target.elements[k].focus();
        return;
      }
    }
    const btn = ev.target.querySelector("button[type=submit]");
    btn.disabled = true;
    try {
      await post("api/settings", { changes });
      saved("Saved.");
      dirty = false;
      load();
    } catch (e) {
      saved("Not saved: " + e.message, true);
    } finally {
      btn.disabled = false;
    }
  });

  async function load() {
    const up = $("#updated");
    try {
      const r = await fetch("api/state", { cache: "no-store" });
      if (!r.ok) throw new Error(r.statusText);
      const s = await r.json();
      token = s.token;
      $("#where").textContent = location.host;
      renderFigures(s);
      renderRuns(s.runs);
      renderLimits(s);
      renderRules(s.rules, s.learned);
      fillSettings(s);
      up.classList.remove("off");
      up.textContent = "updated " + new Date(s.now).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
    } catch (e) {
      up.classList.add("off");
      up.textContent = "Lost contact. Run /p0ssess:p0ssess-me again.";
    }
  }
  load();
  setInterval(load, 10000);
  if (location.hash === "#settings") sheet.showModal();
})();
