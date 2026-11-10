// The page's script: the hero's hand-off loop, the copy buttons, the chart
// bars, and the pinned terminal that replays the scene of the section in view.
// The scenes themselves are static markup in each section; they double as transcripts.
(() => {
  const doc = document, root = doc.documentElement;
  // The same query style.css uses for the two-column layout with the pinned terminal.
  const wide = matchMedia("(min-width: 1024px) and (scripting: enabled)");
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  // Motion only runs under html.anim, which follows the reader's setting.
  const syncMotion = () => root.classList.toggle("anim", !reduce.matches);
  syncMotion();
  reduce.addEventListener("change", syncMotion);

  // Sessions sit in closed disclosures on narrow screens; wide screens keep the story's open (visually hidden) as transcripts.
  const openWrappers = () => { if (wide.matches) doc.querySelectorAll(".story details.scene-wrap").forEach((d) => { d.open = true; }); };
  openWrappers();
  wide.addEventListener("change", openWrappers);

  // Copy buttons ship hidden; they only make sense with a script.
  const status = doc.getElementById("copy-status");
  doc.querySelectorAll(".copy-btn").forEach((btn) => {
    let timer = 0;
    btn.hidden = false;
    btn.addEventListener("click", async () => {
      const code = btn.parentElement.querySelector("code");
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

  // The hero's hand-off. The loop itself is CSS; this finds where the riding ghost lands (the
  // seat in the Haiku pill), names the next measured job each time round, and pauses the loop
  // on request or while it is off screen.
  const hero = doc.querySelector(".hero"), relay = doc.querySelector(".relay");
  if (relay) {
    const rider = relay.querySelector(".rider"), seat = relay.querySelector(".seat");
    const name = relay.querySelector(".name"), job = relay.querySelector(".job"), pause = relay.querySelector(".pause");
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
    const syncRelay = () => { pause.hidden = reduce.matches; place(); };
    syncRelay();
    reduce.addEventListener("change", syncRelay);
    doc.fonts?.ready.then(place);
    if ("ResizeObserver" in window) new ResizeObserver(place).observe(relay);
    rider.addEventListener("animationiteration", (e) => {
      if (e.animationName !== "ride" || !jobs.length) return;
      n = (n + 1) % jobs.length;
      [name.textContent, job.textContent] = jobs[n];
    });
    pause.addEventListener("click", () => {
      const paused = hero.classList.toggle("is-paused");
      pause.textContent = paused ? "Play" : "Pause";
    });
    if ("IntersectionObserver" in window) {
      new IntersectionObserver(([e]) => hero.classList.toggle("is-off", !e.isIntersecting)).observe(relay);
    }
  }

  // Chart bars grow from the baseline once, at one speed, so the shortest lands first and then says it won.
  const charts = [...doc.querySelectorAll(".chart")];
  if ("IntersectionObserver" in window) {
    const seen = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        seen.unobserve(e.target);
        for (const bar of e.target.querySelectorAll("rect.b")) {
          const ms = 240 + parseFloat(bar.getAttribute("width")) * 9;
          bar.style.transitionDuration = ms + "ms";
          bar.closest("li").style.setProperty("--tag-delay", ms - 120 + "ms");
        }
        e.target.classList.add("in");
      }
    }, { threshold: 0.3 });
    charts.forEach((c) => seen.observe(c));
  } else {
    charts.forEach((c) => c.classList.add("in"));
  }

  const term = doc.querySelector(".term");
  if (!term) return;
  const $ = (s) => term.querySelector(s);
  const screen = $(".screen"), log = $(".log"), title = $(".title");
  const gt = $(".gt"), typed = $(".typed"), ph = $(".ph"), me = $(".foot .me");
  const seg = $(".foot .seg"), segText = $(".seg-text"), replay = $(".replay");
  const scenes = [...doc.querySelectorAll(".story figure.scene")];
  const stage = doc.querySelector(".stage"), cap = doc.querySelector(".cap");
  const island = doc.querySelector(".island");
  const focus = island?.querySelector(".focus");
  const liveText = island?.querySelector(".live-text");
  let run = 0, current = null, pending = 0, liveTimer = 0;

  function markSection(fig) {
    const id = fig.closest(".sec")?.id;
    island?.querySelectorAll("a").forEach((a) => {
      const on = a.getAttribute("href") === "#" + id;
      a.setAttribute("aria-current", on ? "true" : "false");
      // The violet focus dot slides to the section's dot; its slower twin makes the stretch.
      if (on && focus) {
        focus.style.setProperty("--x", a.offsetLeft + a.offsetWidth / 2 + "px");
        island.classList.add("ready");
      }
    });
  }

  // The scene's own caption, shown under the terminal.
  function caption(fig) {
    if (!cap) return;
    const text = (fig.querySelector("figcaption")?.textContent || "").replace(/^Transcript:\s*/, "");
    cap.textContent = text.charAt(0).toUpperCase() + text.slice(1);
    cap.classList.remove("swap");
    void cap.offsetWidth;
    cap.classList.add("swap");
  }

  // While work is out with the possessed Haiku, the island becomes a live activity. It stays
  // until the next line arrives, then shows that line's credit (the model and its cost) for a moment.
  function setLive(on) {
    island?.classList.toggle("is-live", on);
    stage?.classList.toggle("is-live", on);
  }
  function live(text, ms) {
    if (!island) return;
    clearTimeout(liveTimer);
    liveText.textContent = " " + text;
    setLive(true);
    if (ms) liveTimer = setTimeout(() => setLive(false), ms);
  }

  function setStatus(v) {
    const [state, text = ""] = (v || "idle|").split("|");
    seg.className = "seg " + state;
    seg.hidden = !text;
    segText.textContent = text;
  }

  async function type(text, id) {
    for (const ch of text) {
      if (id !== run) return;
      typed.textContent += ch;
      await sleep(ch === " " ? 45 : 18 + Math.random() * 34);
    }
  }

  async function play(fig) {
    const id = ++run;
    let out = false;
    current = fig;
    markSection(fig);
    caption(fig);
    clearTimeout(liveTimer);
    setLive(false);
    title.textContent = fig.dataset.title || "";
    me.textContent = fig.dataset.me || fig.querySelector(".foot .me")?.textContent || "";
    log.replaceChildren();
    typed.textContent = "";
    gt.textContent = ">";
    ph.textContent = fig.dataset.ph || "";
    ph.hidden = false;
    setStatus(fig.dataset.st);
    const instant = reduce.matches;
    for (const src of fig.querySelectorAll(".log > .l")) {
      if (id !== run) return;
      if (!instant) {
        if (src.classList.contains("in")) {
          await sleep(log.childElementCount ? 650 : 350);
          if (src.dataset.me) me.textContent = src.dataset.me;
          ph.hidden = true;
          gt.textContent = src.classList.contains("bang") ? "!" : ">";
          await type(src.textContent, id);
          await sleep(320);
          if (id !== run) return;
          typed.textContent = "";
          gt.textContent = ">";
          ph.hidden = false;
        } else {
          await sleep(+src.dataset.d || (src.classList.contains("c") ? 70 : 360));
        }
      }
      if (id !== run) return;
      log.append(src.cloneNode(true));
      if (src.dataset.st) setStatus(src.dataset.st);
      if (src.dataset.me) me.textContent = src.dataset.me;
      if (src.classList.contains("ann")) {
        out = true;
        live("Haiku, possessed");
      } else if (out && !src.classList.contains("c")) {
        out = false;
        if (src.dataset.credit) live(src.dataset.credit, 2600); else setLive(false);
      }
      screen.scrollTop = screen.scrollHeight;
    }
  }

  function activate(fig) {
    if (!fig || fig === current || !wide.matches) return;
    clearTimeout(pending);
    // A short delay keeps a fast scroll from starting every scene it passes.
    pending = setTimeout(() => play(fig), current ? 140 : 0);
  }

  const io = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (e.isIntersecting) activate(e.target.querySelector("figure.scene"));
    }
  }, { rootMargin: "-48% 0px -48% 0px" });
  scenes.forEach((f) => io.observe(f.closest(".sec")));
  // The first scene starts as soon as the terminal scrolls into view below the hero, before its section reaches the middle.
  const first = new IntersectionObserver(([e]) => {
    if (!e.isIntersecting) return;
    first.disconnect();
    if (!current) activate(scenes[0]);
  }, { threshold: 0.25 });
  first.observe(term);

  replay.addEventListener("click", () => current && play(current));
  const syncReplay = () => { replay.hidden = reduce.matches; };
  reduce.addEventListener("change", () => { syncReplay(); current && play(current); });
  syncReplay();
})();
