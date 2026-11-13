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

  // How a run's cost was arrived at, from the ledger's cost_basis.
  const BASIS = {
    reported: ["", "OpenRouter's reported cost"],
    "byok-upstream": ["own key", "OpenRouter's fee plus the charge your own provider key was billed, as OpenRouter reported it"],
    tokens: ["from tokens", "priced from the call's tokens at the model's list price; OpenRouter did not report the cost"],
    reserved: ["max held", "counted at the maximum reserved for the call; the cost could not be read or priced"],
  };

  function renderRuns(runs) {
    const box = $("#runs");
    runs = list(runs).slice(0, 7);
    if (!runs.length) {
      box.replaceChildren(el("li", { class: "empty", text: "Nothing handed off yet. Ask Claude to watch a video or make an image." }));
      return;
    }
    box.replaceChildren(...runs.map((r) => el("li", {},
      el("span", { class: "what", text: words(r.use_case || r.route || "run") }, r.status && r.status !== "done" ? el("span", { class: "tag", text: r.status }) : null,
        BASIS[r.cost_basis] && BASIS[r.cost_basis][0] ? el("span", { class: "tag", text: BASIS[r.cost_basis][0], title: BASIS[r.cost_basis][1] }) : null),
      el("span", { class: "who", text: short(r.model) }),
      el("span", { class: "cost", text: cost(r.cash_usd) + (r.estimated ? "*" : ""), title: BASIS[r.cost_basis] ? BASIS[r.cost_basis][1] : null }),
      el("span", { class: "when", text: ago(r.ts) }))));
  }

  function renderRecords(o) {
    const note = $("#records-note"), box = $("#records");
    if (!o || !o.send) {
      note.textContent = "Off. Nothing is built or sent.";
      box.replaceChildren();
      return;
    }
    const lines = list(o.pending);
    note.textContent = lines.length
      ? "Next weekly batch, exactly as it will be sent. No prompts, files, paths or exact costs."
      : "Nothing pending. Each finished week's runs show here before they are sent.";
    box.replaceChildren(...lines.slice(0, 50).map((l) => el("li", { text: l })));
    if (lines.length > 50) box.append(el("li", { class: "empty", text: "and " + (lines.length - 50) + " more" }));
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
      "outcomes.send": s.outcomes && s.outcomes.send,
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

  // Savings: per task kind, Claude alone against Claude with p0ssess. Only measured numbers are shown.
  const SVG = "http://www.w3.org/2000/svg";
  const svg = (tag, attrs = {}, ...kids) => {
    const n = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attrs)) if (v != null) n.setAttribute(k, v);
    for (const k of kids) if (k != null) n.append(k);
    return n;
  };
  const sig = (v) => Math.ceil(-Math.log10(v)) + 1;
  const usd = (v) => {
    if (v >= 1000) return "$" + Math.round(v).toLocaleString("en-US");
    if (v >= 1) return "$" + v.toFixed(2);
    if (v <= 0) return "$0";
    if (v < 0.0001) return "<$0.0001";
    return "$" + v.toFixed(sig(v));
  };
  const secs = (v) => (v < 10 ? v.toFixed(1) + " s" : v < 90 ? Math.round(v) + " s" : (v / 60).toFixed(1) + " min");
  const share = (v) => {
    const p = v * 100;
    if (p >= 10) return Math.round(p) + "%";
    if (p >= 1) return p.toFixed(1) + "%";
    if (p <= 0) return "0%";
    if (p < 0.001) return "<0.001%";
    return p.toFixed(sig(p)) + "%";
  };
  const times = (r) => (r >= 10 ? Math.round(r) : r.toFixed(1)) + "×";
  const compare = (a, b, less, more) => {
    if (a <= 0 && b <= 0) return "the same";
    if (b <= 0) return "nothing with p0ssess";
    if (a / b >= 2) return times(a / b) + " " + less;
    if (a / b >= 1.05) return Math.round((1 - b / a) * 100) + "% " + less;
    if (b / a >= 2) return times(b / a) + " " + more;
    if (b / a >= 1.05) return Math.round((b / a - 1) * 100) + "% " + more;
    return "about the same";
  };
  const runsText = (n) => (n === 1 ? "1 run" : n + " runs");
  const hasCost = (s) => !!s && s.correct > 0 && s.cost_per_correct_usd != null;
  const costText = (s) => (!s ? "not measured" : hasCost(s) ? usd(s.cost_per_correct_usd) : "no correct answers in " + runsText(s.runs));
  const dated = (iso) => {
    const d = iso ? new Date(iso) : null;
    return d && d.getFullYear() > 1 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) : "";
  };
  const SIDES = [["alone", "Claude alone"], ["with", "With p0ssess"]];
  let report = null;

  // costBars draws one pair of bars on one shared linear axis, scaled to the larger of the two.
  function costBars(k, width) {
    const bar = 12, gap = 8, room = 76;
    const vals = SIDES.map(([side]) => (hasCost(k[side]) ? k[side].cost_per_correct_usd : null));
    const top = Math.max(0, ...vals.filter((v) => v != null));
    const span = Math.max(40, width - room);
    const h = SIDES.length * bar + (SIDES.length - 1) * gap;
    const out = svg("svg", { class: "bars", width, height: h, viewBox: "0 0 " + width + " " + h, role: "img",
      "aria-label": "Cost per correct answer: " + SIDES.map(([side, name]) => name + " " + costText(k[side])).join(", ") });
    SIDES.forEach(([side, name], i) => {
      const y = i * (bar + gap), v = vals[i];
      if (v == null) {
        out.append(svg("text", { class: "none", x: 6, y: y + bar - 2 }, costText(k[side])));
        return;
      }
      const w = top > 0 ? Math.max(2, (v / top) * span) : 2, r = Math.min(4, w / 2);
      const d = "M0 " + y + "h" + (w - r) + "a" + r + " " + r + " 0 0 1 " + r + " " + r + "v" + (bar - 2 * r) + "a" + r + " " + r + " 0 0 1 " + -r + " " + r + "H0z";
      const s = k[side];
      out.append(svg("path", { class: "bar " + side, d }, svg("title", {}, name + ": " + usd(v) + " per correct answer, " + s.correct + " of " + runsText(s.runs) + " right")));
      out.append(svg("text", { x: w + 6, y: y + bar - 2 }, usd(v)));
    });
    out.append(svg("line", { class: "base", x1: 0.5, x2: 0.5, y1: -3, y2: h + 3 }));
    return out;
  }

  function pairs(k, label, text, verdict) {
    return el("div", { class: "metric" }, el("h3", { text: label }),
      ...SIDES.map(([side]) => {
        const t = text(k[side]);
        return el("p", { class: "pair" + (t.plain ? " none" : "") }, el("span", { class: "key " + side, "aria-hidden": "true" }), t.text);
      }),
      verdict ? el("p", { class: "verdict", text: verdict }) : null);
  }

  function kindRow(k) {
    const a = k.alone, w = k.with;
    const saving = hasCost(a) && hasCost(w) ? compare(a.cost_per_correct_usd, w.cost_per_correct_usd, "cheaper", "more expensive") : "";
    const fast = a && w && a.runs && w.runs ? compare(a.mean_seconds, w.mean_seconds, "faster", "slower") : "";
    const ws = (s) => s && s.weekly_share_per_correct != null ? s.weekly_share_per_correct : null;
    const less = ws(a) != null && ws(w) != null ? compare(ws(a), ws(w), "less", "more") : "";
    const notes = [];
    if (!a) notes.push("Claude alone was not measured yet.");
    if (!w) notes.push("With p0ssess was not measured yet.");
    if (w && w.source === "calibration") notes.push("With p0ssess: model call only. Claude's handoff is not counted.");
    return el("li", { class: "kind" },
      el("div", { class: "name" }, el("p", { class: "what", text: words(k.use_case) }),
        k.task ? el("p", { class: "task", text: k.task }) : null,
        k.model ? el("p", { class: "to", text: "to " + short(k.model), title: k.model }) : null),
      el("div", { class: "chart" }, el("h3", { text: "Cost" }), el("div", { class: "bars-slot" }),
        saving ? el("p", { class: "verdict", text: saving }) : null,
        ...notes.map((n) => el("p", { class: "only", text: n }))),
      pairs(k, "Mean time", (s) => (s && s.runs ? { text: secs(s.mean_seconds) } : { text: "not measured", plain: true }), fast),
      pairs(k, "Weekly limit", (s) => (!s ? { text: "not measured", plain: true } : ws(s) == null ? { text: "not read", plain: true } : { text: share(ws(s)) }), less));
  }

  // modelTable groups the calibration rows under their task kind.
  function modelTable(models) {
    const head = el("tr", {}, ...["Model", "Correct", "Cost per correct", "Mean time"].map((t, i) => el("th", { class: i ? "num" : null, scope: "col", text: t })));
    const rows = [];
    models.forEach((m, i) => {
      if (!i || models[i - 1].use_case !== m.use_case) rows.push(el("tr", { class: "group" }, el("th", { colspan: "4", scope: "rowgroup", text: words(m.use_case) })));
      rows.push(el("tr", {},
        el("td", { class: "model", title: m.model }, short(m.model), m.picked ? el("span", { class: "tag", text: "routed" }) : null),
        el("td", { class: "num", text: m.correct + " of " + m.runs }),
        el("td", { class: "num", text: m.correct > 0 && m.cost_per_correct_usd != null ? usd(m.cost_per_correct_usd) : "none correct" }),
        el("td", { class: "num", text: secs(m.mean_seconds) })));
    });
    return el("div", { class: "table-wrap" }, el("table", { class: "models" }, el("thead", {}, head), el("tbody", {}, ...rows)));
  }

  const empty = (lead, ...hint) => el("div", { class: "savings-empty" }, el("p", { text: lead }), el("p", {}, ...hint));
  const code = (t) => el("code", { text: t });

  function renderSavings(r) {
    const body = $("#savings-body"), notes = $("#savings-notes"), pdf = $("#savings-pdf"), when = $("#savings-when");
    const kinds = list(r.kinds), models = list(r.models);
    const any = kinds.length || models.length;
    pdf.hidden = !(r.pro && any);
    notes.replaceChildren();
    if (!r.pro) {
      when.textContent = "";
      body.replaceChildren(empty("The savings report is part of p0ssess Pro.", "Run ", code("/p0ssess:pro"), " in Claude Code for details."));
      return;
    }
    if (!any) {
      when.textContent = "";
      body.replaceChildren(empty("Nothing has been measured yet.",
        "Calibration runs each task kind on samples with known answers, with Claude alone and with p0ssess, and records what a correct answer cost and how long it took. Run ",
        code("/p0ssess:calibrate"), " to start. It shows what the runs will cost before anything runs."));
      return;
    }
    const cal = dated(r.calibrated), base = dated(r.baselined);
    when.textContent = [cal && "Calibrated " + cal, base ? "Claude baseline " + base : "Claude alone not measured yet"].filter(Boolean).join(" · ");
    const kids = [];
    const baseline = kinds.some((k) => k.alone);
    if (kinds.length) {
      kids.push(el("ul", { class: "legend", "aria-label": "Legend" }, ...SIDES.map(([side, name]) => el("li", {}, el("span", { class: "key " + side, "aria-hidden": "true" }), name)),
        el("li", { class: "per", text: "Cost and weekly limit are per correct answer" })));
      kids.push(el("ul", { class: "kinds" }, ...kinds.map(kindRow)));
    }
    if (!baseline) kids.push(el("p", { class: "note", text: "Claude alone has not been measured yet, so there is nothing to compare against." }));
    const t = r.totals;
    if (t && t.kinds > 0) {
      let s = "Over the " + (t.kinds === 1 ? "1 kind" : t.kinds + " kinds") + " measured both ways, one correct answer of each cost " + usd(t.alone_usd) + " with Claude alone and " +
        usd(t.with_usd) + " with p0ssess (" + compare(t.alone_usd, t.with_usd, "cheaper", "more expensive") + "). Time: " + secs(t.alone_seconds) + " alone, " + secs(t.with_seconds) + " with p0ssess.";
      if (t.weekly_share_saved != null) s += " Weekly limit saved: " + share(t.weekly_share_saved) + ".";
      kids.push(el("p", { class: "totals", text: s }));
    }
    if (models.length) kids.push(el("h3", { class: "next", text: "Models measured" }), modelTable(models));
    body.replaceChildren(...kids);
    body.querySelectorAll(".bars-slot").forEach((slot, i) => slot.append(costBars(kinds[i], Math.max(160, Math.floor(slot.clientWidth)))));
    const prices = list(r.prices);
    const small = list(r.notes).slice();
    if (prices.length) small.push("Claude API prices used, per million tokens: " + prices.map((p) => p.model + " " + usd(p.in) + " in, " + usd(p.out) + " out, " + usd(p.cache_read) + " cache read").join("; ") + ".");
    notes.replaceChildren(...small.map((n) => el("li", { text: n })));
  }

  async function loadSavings() {
    try {
      const r = await fetch("api/savings", { cache: "no-store" });
      if (!r.ok) throw new Error(r.statusText);
      report = await r.json();
      renderSavings(report);
    } catch (e) {
      if (!report) $("#savings-body").replaceChildren(el("p", { class: "empty", text: "Couldn't load the savings report." }));
    }
  }
  let lastWidth = 0;
  new ResizeObserver(() => {
    const w = $("#savings-body").clientWidth;
    if (report && Math.abs(w - lastWidth) > 8) { lastWidth = w; renderSavings(report); }
  }).observe($("#savings-body"));

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
      renderRecords(s.outcomes);
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
  loadSavings();
  setInterval(loadSavings, 60000);
  if (location.hash === "#settings") sheet.showModal();
})();
