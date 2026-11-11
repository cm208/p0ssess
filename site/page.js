// The page's script: release links, copy buttons, the hairlines and chart bars that draw when
// they scroll into view, the island, the hand-off figure and the film test in the hero.
// Everything it animates has a complete still state in the markup; with scripts off or with
// reduced motion the page shows that.
(() => {
  const doc = document, root = doc.documentElement;
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const $ = (s, el = doc) => el.querySelector(s);
  const $$ = (s, el = doc) => [...el.querySelectorAll(s)];
  const hasIO = "IntersectionObserver" in window;

  // Motion only runs under html.anim, which follows the reader's setting.
  const syncMotion = () => root.classList.toggle("anim", !reduce.matches);
  syncMotion();
  reduce.addEventListener("change", syncMotion);

  // Release links. release.js holds the addresses; until it names a repository and a version,
  // the download controls stay disabled and no outside link shows.
  const rel = window.P0SSESS || {};
  const repo = String(rel.repo || "").replace(/\/+$/, ""), version = String(rel.version || "");
  const docs = String(rel.docs || "").replace(/\/+$/, "");
  const link = (a, href) => { a.href = href; a.removeAttribute("aria-disabled"); a.hidden = false; };
  if (docs) {
    $$('[data-link="docs"]').forEach((a) => link(a, docs));
    // A page of the documentation site, such as "media/#run-b-on-054".
    $$("[data-doc]").forEach((a) => link(a, docs + "/" + a.dataset.doc));
  }
  if (repo) {
    $$('[data-link="repo"]').forEach((a) => link(a, repo));
    $$("[data-repo-url]").forEach((el) => { el.textContent = repo + ".git"; });
  }
  if (repo && version) {
    const files = `${repo}/releases/download/v${version}/`;
    $$("[data-asset]").forEach((a) => link(a, files + a.dataset.asset.replace("{version}", version)));
    $$('[data-link="release"]').forEach((a) => link(a, `${repo}/releases/tag/v${version}`));
    $$("[data-archive-name]").forEach((el) => { el.textContent = `p0ssess-${version}.tar.gz`; });
    $$("[data-release-state]").forEach((el) => { el.textContent = `p0ssess ${version}, served from the repository's releases.`; });
    $$("[data-sum]").forEach((el) => {
      const sum = (rel.sha256 || {})[el.dataset.sum];
      if (sum) { el.textContent = sum; el.hidden = false; }
    });
  }

  // Copy buttons ship hidden; they only make sense with a script, and only for a command that is complete.
  const status = doc.getElementById("copy-status");
  $$(".copy-btn").forEach((btn) => {
    if (!repo && $("[data-repo-url]", btn.parentElement)) return;
    let timer = 0;
    btn.hidden = false;
    btn.addEventListener("click", async () => {
      const code = $("code", btn.parentElement);
      let copied = true;
      try {
        await navigator.clipboard.writeText(code.textContent);
      } catch {
        // No clipboard access (an insecure origin, or permission denied): select the command instead.
        copied = false;
        const range = doc.createRange();
        range.selectNodeContents(code);
        const sel = getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
      }
      btn.textContent = copied ? "Copied" : "Selected";
      if (status) status.textContent = copied ? "Copied." : "Selected. Copy it with your keyboard.";
      clearTimeout(timer);
      timer = setTimeout(() => { btn.textContent = "Copy"; if (status) status.textContent = ""; }, 1800);
    });
  });

  // Hairlines draw out of their violet start, and chart bars grow from the baseline, once each,
  // when they scroll into view. Bars move at one speed, so the shortest lands first and then says it won.
  const growBars = (chart) => {
    for (const bar of $$("rect.b", chart)) {
      const ms = 240 + parseFloat(bar.getAttribute("width")) * 9;
      bar.style.transitionDuration = ms + "ms";
      bar.closest("li").style.setProperty("--tag-delay", ms - 120 + "ms");
    }
    chart.classList.add("in");
  };
  const lines = $$(".hl, .sec > .wrap");
  if (hasIO) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        if (e.target.classList.contains("chart")) growBars(e.target); else e.target.classList.add("in");
      }
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
    lines.forEach((el) => seen.observe(el));
  } else {
    lines.forEach((el) => el.classList.add("in"));
  }
  // The two answers under the abilities play once their box is mostly on screen.
  const results = $$(".result");
  if (hasIO) {
    const back = new IntersectionObserver((entries) => {
      for (const e of entries) if (e.isIntersecting) { back.unobserve(e.target); e.target.classList.add("in"); }
    }, { threshold: 0.6 });
    results.forEach((el) => back.observe(el));
  } else {
    results.forEach((el) => el.classList.add("in"));
  }
  // The usage chart has two readings; the bars of the one just chosen grow again.
  const usage = $(".usage");
  if (usage) {
    usage.addEventListener("change", () => {
      if (reduce.matches || !usage.classList.contains("in")) return;
      usage.classList.remove("in");
      void usage.offsetWidth;
      growBars(usage);
    });
  }

  // The island: shown once the top bar has scrolled away. Its violet dot slides to the section
  // in view (a slower twin makes the stretch) and the pill resizes to that section's name.
  const island = $(".island"), topbar = $(".topbar");
  if (island && topbar && hasIO) {
    const focus = $(".focus", island), label = $(".isl-label", island), text = $("span", label);
    const dots = $$("ol a", island);
    const mark = (a) => {
      dots.forEach((d) => d.setAttribute("aria-current", d === a ? "true" : "false"));
      focus.style.setProperty("--x", a.offsetLeft + a.offsetWidth / 2 + "px");
      island.classList.add("ready");
      island.classList.toggle("end", a.getAttribute("href") === "#try");
      if (text.textContent !== a.getAttribute("aria-label")) {
        text.textContent = a.getAttribute("aria-label");
        label.style.setProperty("--lw", text.offsetWidth + "px");
      }
    };
    island.hidden = false;
    new IntersectionObserver(([e]) => island.classList.toggle("on", !e.isIntersecting)).observe(topbar);
    const owner = new Map();
    const spy = new IntersectionObserver((entries) => {
      for (const e of entries) if (e.isIntersecting) mark(owner.get(e.target));
    }, { rootMargin: "-45% 0px -50% 0px" });
    dots.forEach((d) => {
      const t = doc.getElementById(d.getAttribute("href").slice(1));
      if (!t) return;
      const box = t.closest(".sec, .hero") || t;
      owner.set(box, d);
      spy.observe(box);
    });
    mark(dots[0]);
    doc.fonts?.ready.then(() => { const a = dots.find((d) => d.getAttribute("aria-current") === "true"); if (a) { text.textContent = ""; mark(a); } });
  }

  // The hand-off. The timeline is CSS; this finds where the riding ghost lands (the seat in the
  // Haiku pill), plays it once when it scrolls into view, and names the next measured job on each replay.
  const relay = $(".relay");
  if (relay) {
    const rider = $(".rider", relay), seat = $(".seat", relay);
    const name = $(".name", relay), job = $(".job", relay), back = $(".back", relay), replay = $(".replay", relay);
    let jobs = [], n = 0;
    try { jobs = JSON.parse(relay.dataset.jobs); } catch { /* keeps the job in the markup */ }
    const center = (el) => {
      let x = el.offsetWidth / 2, y = el.offsetHeight / 2;
      for (let e = el; e && e !== relay; e = e.offsetParent) { x += e.offsetLeft; y += e.offsetTop; }
      return [x, y];
    };
    const place = () => {
      if (!rider.offsetParent) return;
      const [rx, ry] = center(rider), [sx, sy] = center(seat);
      relay.style.setProperty("--rx", sx - rx + "px");
      relay.style.setProperty("--ry", sy - ry + "px");
      relay.style.setProperty("--rs", (seat.offsetWidth / rider.offsetWidth).toFixed(4));
    };
    const play = () => {
      relay.classList.remove("play", "done");
      void relay.offsetWidth;
      place();
      relay.classList.add("play");
    };
    rider.addEventListener("animationend", (e) => {
      if (e.animationName !== "ride") return;
      relay.classList.add("done");
      relay.classList.remove("play");
    });
    replay.addEventListener("click", () => {
      if (jobs.length) {
        n = (n + 1) % jobs.length;
        [name.textContent, job.textContent] = jobs[n];
        back.textContent = `(${jobs[n][0]}, ${jobs[n][2]})`;
      }
      play();
    });
    const syncRelay = () => { replay.hidden = reduce.matches; if (reduce.matches) relay.classList.remove("play"); };
    syncRelay();
    reduce.addEventListener("change", syncRelay);
    doc.fonts?.ready.then(place);
    if ("ResizeObserver" in window) new ResizeObserver(place).observe(relay);
    if (hasIO) {
      const once = new IntersectionObserver(([e]) => {
        if (!e.isIntersecting) return;
        once.disconnect();
        if (!reduce.matches) play();
      }, { threshold: 0.6 });
      once.observe(relay);
    }
  }

  // The film test. One prompt was run twice on a film of 596.46 seconds. The markup holds both
  // results and the ten scored events; this turns the events into tabs, lets the pointer scrub
  // the film, and plays the sweep from "Claude alone" to "with p0ssess" once the timeline is in view.
  const film = $(".film");
  if (!film) return;
  const DUR = 596.46, COLS = 8, FW = 96, FH = 54, STEP = 15, FIRST = 7.5, UNIFORM = 40;
  const alone = $("#mode-alone", film), poss = $("#mode-poss", film);
  const tracks = $(".tracks", film), screen = $(".screen", film), lanes = $(".lanes", film), panel = $(".said", film), tc = $(".tc", film);
  const canvas = $(".reel", film), still = $(".still", film), ghost = $(".head .g", film);
  const replayFilm = $(".readout .replay", film), statusLine = $("#film-status", film);
  const evs = $$(".moments > .ev", film), lit = $$(".evs-lit > i", film);
  const fmt = (t) => Math.floor(t / 60) + ":" + String(Math.floor(t % 60)).padStart(2, "0");
  const io = (x) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2);

  // Each event, read from its list item.
  const items = evs.map((li, i) => ({
    li, lit: lit[i], miss: li.classList.contains("miss"), featured: li.hasAttribute("data-featured"),
    at: +li.dataset.at, frame: +li.dataset.f, qt: li.dataset.qt,
    when: $(".ev-when", li).textContent, what: $(".ev-what", li).textContent,
    alone: $(".ev-alone", li).textContent, quote: $(".ev-poss", li).textContent,
    note: $(".ev-note", li)?.textContent || "",
  }));
  // Where each event begins, from the same shares the lanes are drawn with.
  const shares = [30, 25, 60, 35, 35, 40, 75, 70, 115, 111.5];
  const starts = shares.map((_, i) => shares.slice(0, i).reduce((a, b) => a + b, 0));
  const eventAt = (t) => { let k = 0; starts.forEach((s, i) => { if (t >= s) k = i; }); return k; };

  // What the sweep recovers, and when the playhead passes it: two sound cues Claude alone could
  // only guess at (one point each) and the four events between its stills (two points each).
  const gains = [[6, 0, 1], [20, 0, 1], [150, 2, 0], [185, 2, 0], [300, 2, 0], [370, 2, 0]];
  const tally = (t, full) => {
    let ev = 12, snd = 3;
    for (const [at, e, s] of gains) if (full || t >= at) { ev += e; snd += s; }
    return { ev, snd, score: ev + snd };
  };

  // The readouts the script takes over. Each starts as a pair of values, one per mode.
  const out = {};
  $$("[data-k]", film).forEach((el) => { out[el.dataset.k] = el; });
  const put = (k, v) => { if (out[k].textContent !== String(v)) { out[k].textContent = v; return true; } return false; };
  let shown = -1;
  const readout = (t, full, quiet) => {
    const s = full ? tally(DUR, true) : tally(t, false);
    put("ev", s.ev);
    put("snd", s.snd);
    if (put("score", s.score) && !quiet && shown !== -1 && s.score > shown) {
      out.score.classList.remove("tick");
      void out.score.offsetWidth;
      out.score.classList.add("tick");
    }
    shown = s.score;
    put("cost", full ? "$0.1140" : "$0.0999");
  };

  // Frames come from one sprite: 40 at an even 15 seconds, then a poster for each event.
  const ctx = canvas.getContext("2d");
  const sprite = new Image();
  let spriteOk = false, frame = -1;
  const draw = (i) => {
    if (!spriteOk || i === frame) return;
    frame = i;
    ctx.drawImage(sprite, (i % COLS) * FW, Math.floor(i / COLS) * FH, FW, FH, 0, 0, FW, FH);
    canvas.hidden = false;
    still.hidden = true;
  };
  const frameAt = (t) => Math.max(0, Math.min(UNIFORM - 1, Math.round((t - FIRST) / STEP)));

  let sel = Math.max(0, items.findIndex((it) => it.featured)), run = 0, sweeping = false, scrubbing = false;
  // The numbers are set on the parts that draw with them, not on the whole hero.
  const setHead = (pct) => tracks.style.setProperty("--h", pct.toFixed(3));
  const setP = (pct) => { for (const el of [tracks, screen]) el.style.setProperty("--p", pct.toFixed(3)); };
  const blink = () => { if (reduce.matches) return; ghost.classList.remove("blink"); void ghost.offsetWidth; ghost.classList.add("blink"); };

  function fill(i) {
    const it = items[i];
    $(".when", panel).textContent = it.when;
    $(".what", panel).textContent = it.what;
    const a = $(".by-alone", panel);
    a.classList.toggle("miss", it.miss);
    $("dd", a).textContent = it.alone;
    $(".by-poss q", panel).textContent = it.quote;
    $(".qt", panel).textContent = it.qt;
    const note = $(".said-note", panel);
    note.textContent = it.note;
    note.hidden = !it.note;
    film.toggleAttribute("data-miss", it.miss);
    panel.setAttribute("aria-labelledby", "ev-tab-" + i);
  }
  // Choose an event: its tab, its panel, and (unless the pointer is scrubbing) its poster frame.
  function select(i, opts = {}) {
    sel = i;
    items.forEach((it, k) => {
      const on = k === i;
      it.li.classList.toggle("sel", on);
      it.lit.classList.toggle("sel", on);
      it.tab.setAttribute("aria-selected", on ? "true" : "false");
      it.tab.tabIndex = on ? 0 : -1;
    });
    fill(i);
    if (opts.park !== false) {
      const it = items[i];
      setHead((it.at / DUR) * 100);
      tc.textContent = fmt(it.at);
      draw(it.frame);
    }
  }

  // The events become tabs for one panel.
  const list = $(".moments", film);
  list.setAttribute("role", "tablist");
  list.setAttribute("aria-orientation", "horizontal");
  panel.setAttribute("role", "tabpanel");
  items.forEach((it, i) => {
    it.li.setAttribute("role", "presentation");
    $$(":scope > span, :scope > q", it.li).forEach((el) => { el.hidden = true; });
    const b = doc.createElement("button");
    b.type = "button";
    b.id = "ev-tab-" + i;
    b.setAttribute("role", "tab");
    b.setAttribute("aria-controls", "said");
    b.setAttribute("aria-label", `${it.when}. ${it.what}. Claude alone ${it.miss ? "missed it" : "found it"}.`);
    b.addEventListener("click", () => select(i));
    it.li.append(b);
    it.tab = b;
  });
  list.addEventListener("keydown", (e) => {
    const move = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    let i = sel;
    if (move) i = (sel + move + items.length) % items.length;
    else if (e.key === "Home") i = 0;
    else if (e.key === "End") i = items.length - 1;
    else return;
    e.preventDefault();
    select(i);
    items[i].tab.focus();
  });
  const step = $(".step", film);
  step.hidden = false;
  step.addEventListener("click", (e) => {
    const b = e.target.closest("[data-step]");
    if (b) select((sel + +b.dataset.step + items.length) % items.length);
  });

  // Scrubbing: the playhead follows the pointer along the film and the frame follows the playhead.
  const timeAt = (e) => {
    const r = lanes.getBoundingClientRect();
    return Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)) * DUR;
  };
  const scrub = (e) => {
    if (sweeping) return;
    const t = timeAt(e);
    if (!scrubbing) { scrubbing = true; film.classList.add("live"); }
    setHead((t / DUR) * 100);
    tc.textContent = fmt(t);
    draw(frameAt(t));
    const k = eventAt(t);
    if (k !== sel) select(k, { park: false });
  };
  const settle = () => {
    if (!scrubbing) return;
    scrubbing = false;
    film.classList.remove("live");
    select(sel);
  };
  lanes.addEventListener("pointermove", (e) => { if (e.pointerType === "mouse" || e.buttons) scrub(e); });
  lanes.addEventListener("pointerdown", (e) => { if (e.pointerType !== "mouse") scrub(e); });
  lanes.addEventListener("pointerleave", settle);
  lanes.addEventListener("pointerup", (e) => { if (e.pointerType !== "mouse") settle(); });
  lanes.addEventListener("pointercancel", settle);

  // The two modes. Going to "with p0ssess" is the sweep: the ghost rides the playhead across
  // the film, the lit layers follow it, and the score ticks up as it passes what was missed.
  function say() {
    const s = tally(DUR, poss.checked);
    statusLine.textContent = poss.checked
      ? `With p0ssess: ${s.score} of 25. Events ${s.ev} of 20, sound cues ${s.snd} of 5, total cost $0.1140.`
      : `Claude alone: ${s.score} of 25. Events ${s.ev} of 20, sound cues ${s.snd} of 5, total cost $0.0999.`;
  }
  function rest(full) {
    run++;
    sweeping = false;
    film.classList.remove("live");
    setP(full ? 100 : 0);
    readout(0, full, true);
    select(sel);
  }
  function sweep() {
    const id = ++run, t0 = performance.now(), ms = 2400;
    sweeping = true;
    film.classList.add("live");
    blink();
    const tick = (now) => {
      if (id !== run) return;
      const x = Math.min(1, (now - t0) / ms), pct = io(x) * 100, t = (pct / 100) * DUR;
      setP(pct);
      setHead(pct);
      tc.textContent = fmt(t);
      draw(frameAt(t));
      readout(t, x === 1);
      if (x < 1) return requestAnimationFrame(tick);
      // The ghost has seen the film through; it walks back to the featured event.
      sweeping = false;
      setTimeout(() => {
        if (id !== run) return;
        film.classList.remove("live");
        select(sel);
        blink();
        say();
      }, 320);
    };
    requestAnimationFrame(tick);
  }
  const onMode = () => {
    if (poss.checked && !reduce.matches) sweep(); else { rest(poss.checked); say(); }
  };
  alone.addEventListener("change", onMode);
  poss.addEventListener("change", onMode);

  // Start. With motion allowed the film opens on "Claude alone" and sweeps once its timeline is
  // on screen; with reduced motion it stays on the finished frame.
  film.classList.add("js");
  $$("[data-k]", film).forEach((el) => { el.textContent = $(".v-poss", el).textContent; });
  sprite.addEventListener("load", () => { spriteOk = true; frame = -1; if (!sweeping && !scrubbing) draw(items[sel].frame); });
  sprite.src = "film-frames.webp";
  const intro = () => {
    alone.checked = true;
    rest(false);
    root.classList.add("film-ready");
    const go = () => { if (!alone.checked) return; poss.checked = true; sweep(); };
    if (!hasIO) return void setTimeout(go, 1100);
    const when = new IntersectionObserver(([e]) => {
      if (!e.isIntersecting) return;
      when.disconnect();
      setTimeout(go, 1100);
    }, { threshold: 0.75 });
    when.observe(lanes);
  };
  if (reduce.matches) { rest(true); root.classList.add("film-ready"); } else intro();
  replayFilm.hidden = reduce.matches;
  replayFilm.addEventListener("click", () => {
    alone.checked = true;
    rest(false);
    setTimeout(() => { if (alone.checked) { poss.checked = true; sweep(); } }, 700);
  });
  reduce.addEventListener("change", () => { replayFilm.hidden = reduce.matches; if (reduce.matches) { poss.checked = true; rest(true); } });
})();
