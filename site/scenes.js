// Replays the scene of the section in view in the pinned terminal.
// The scenes themselves are static markup in each section; they double as transcripts.
(() => {
  const doc = document;
  // Sessions sit in closed disclosures on narrow screens; wide screens keep them open (visually hidden) as transcripts.
  // The same query style.css uses for the two-column layout with the pinned terminal.
  const wideNow = matchMedia("(min-width: 1024px) and (scripting: enabled)");
  const openWrappers = () => { if (wideNow.matches) doc.querySelectorAll("details.scene-wrap").forEach((d) => { d.open = true; }); };
  openWrappers();
  wideNow.addEventListener("change", openWrappers);

  const term = doc.querySelector(".term");
  if (!term) return;
  const $ = (s) => term.querySelector(s);
  const screen = $(".screen"), log = $(".log"), title = $(".title");
  const gt = $(".gt"), typed = $(".typed"), ph = $(".ph"), me = $(".foot .me");
  const seg = $(".foot .seg"), segText = $(".seg-text"), replay = $(".replay");
  const scenes = [...doc.querySelectorAll("figure.scene")];
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const wide = wideNow;

  const island = doc.querySelector(".island");
  const liveText = island?.querySelector(".live-text");
  let run = 0, current = null, pending = 0, liveTimer = 0;

  function markSection(fig) {
    const id = fig.closest(".sec")?.id;
    island?.querySelectorAll("a").forEach((a) => a.setAttribute("aria-current", a.getAttribute("href") === "#" + id ? "true" : "false"));
  }

  // A hand-off grows out of the island for a moment, like a live activity.
  function live(text) {
    if (!island) return;
    clearTimeout(liveTimer);
    liveText.textContent = " " + text;
    island.classList.add("is-live");
    liveTimer = setTimeout(() => island.classList.remove("is-live"), 2600);
  }
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

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
    current = fig;
    markSection(fig);
    island?.classList.remove("is-live");
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
      if (src.classList.contains("ann")) live("handing to " + src.textContent.replace(/^handing this to ([^:]+):.*$/, "$1"));
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

  replay.addEventListener("click", () => current && play(current));
  const syncReplay = () => { replay.hidden = reduce.matches; };
  reduce.addEventListener("change", () => { syncReplay(); current && play(current); });
  wide.addEventListener("change", () => { if (wide.matches && !current) activate(scenes[0]); });
  syncReplay();
  if (wide.matches) activate(scenes[0]);
})();
