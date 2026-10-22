(() => {
  const SURFACE_SEL = "[data-menu-reference-surface]";

  function fail(msg) {
    throw new Error(msg);
  }

  function ready() {
    if (document.querySelector(SURFACE_SEL) === null) return false;
    return document.querySelectorAll("x-dc").length === 0;
  }

  async function motionSettled(root) {
    // root: the actual surface or its mobile viewport subtree; never the
    // whole document on desktop.
    for (;;) {
      const touching = document.getAnimations().filter((anim) => {
        const effect = anim.effect;
        const target = effect && effect.target;
        if (!(target instanceof Element)) return false;
        if (target !== root && !root.contains(target)) return false;
        const timing = effect.getComputedTiming();
        if (timing.iterations === Infinity) return false; // cursors, spinners
        return anim.pending || anim.playState === "running";
      });
      if (touching.length === 0) return;
      // Cancelation rejects finished; treat that as settled and recheck.
      await Promise.all(touching.map((anim) => anim.finished.catch(() => {})));
      // Late fonts/runtime may start another finite animation; loop once more.
    }
  }

  function begin() {
    const surface = document.querySelector(SURFACE_SEL);
    const viewport = surface && surface.closest("[data-menu-reference-viewport]");
    document.fonts.ready
      .then(() => motionSettled(viewport || surface))
      .then(() => {
        requestAnimationFrame(() => requestAnimationFrame(build));
      });
  }

  function build() {
    const surfaces = document.querySelectorAll(SURFACE_SEL);
    if (surfaces.length !== 1) {
      fail(`expected exactly one surface, found ${surfaces.length}`);
    }
    const surface = surfaces[0];
    const viewport = surface.closest("[data-menu-reference-viewport]");
    let shiftX = 0, shiftY = 0;
    let positioningGuarded = false;

    function guardInitialPositioning() {
      if (!viewport || positioningGuarded) return;
      const cs = getComputedStyle(viewport);
      if (cs.position !== "relative" || cs.transform !== "none" ||
          viewport.style.left !== "" || viewport.style.top !== "") {
        fail("source viewport is not an untouched relative block");
      }
      positioningGuarded = true;
    }

    const label = document.createElement("div");
    label.setAttribute("data-screen-label", "menu");
    label.setAttribute("aria-hidden", "true");
    label.inert = true;
    label.style.cssText = "position:absolute;pointer-events:none;opacity:0;";

    // Fail closed: any throw past this point must leave no measurable label
    // and no screen label on the source surface; only a diagnostic remains.
    function publish(geometry) {
      surface.removeAttribute("data-screen-label");
      for (const [key, value] of Object.entries(geometry)) {
        if (label.style[key] !== value) label.style[key] = value;
      }
      if (!label.isConnected) document.body.appendChild(label);
      label.setAttribute("data-menu-reference-frame", "");
    }
    function retract(err) {
      label.remove();
      label.removeAttribute("data-menu-reference-frame");
      surface.removeAttribute("data-screen-label");
      document.documentElement.setAttribute(
        "data-menu-reference-error",
        String(err && err.message ? err.message : err)
      );
    }

    function normalizeViewport() {
      if (!viewport) return;
      guardInitialPositioning();
      const origin = viewport.getBoundingClientRect();
      if (origin.width !== parseFloat(viewport.style.width) || origin.height !== parseFloat(viewport.style.height)) {
        fail("source viewport differs from its declared dimensions");
      }
      const dx = origin.x + window.scrollX, dy = origin.y + window.scrollY;
      if (dx === 0 && dy === 0) return;
      const nodes = [viewport, ...viewport.querySelectorAll("*")];
      const before = nodes.map(el => el.getBoundingClientRect());
      shiftX -= dx;
      shiftY -= dy;
      viewport.style.position = "relative";
      viewport.style.left = `${shiftX}px`;
      viewport.style.top = `${shiftY}px`;
      viewport.style.zIndex = "1000";
      const translated = viewport.getBoundingClientRect();
      const fmt = (r, o) =>
        `x:${r.x - o.x},y:${r.y - o.y},w:${r.width},h:${r.height}`;
      nodes.forEach((el, index) => {
        const old = before[index], next = el.getBoundingClientRect();
        if (!old.width && !old.height) return;
        if (old.width !== next.width || old.height !== next.height ||
            old.x - origin.x !== next.x - translated.x || old.y - origin.y !== next.y - translated.y) {
          fail(
            "viewport normalization changed source child geometry: " +
            `index=${index} tag=${el.tagName} ` +
            `old=[${fmt(old, origin)}] new=[${fmt(next, translated)}]`
          );
        }
      });
      viewport.dataset.menuViewportNormalized = "";
    }

    function update() {
      try {
        normalizeViewport();
        const before = surface.getBoundingClientRect();
        const left = Math.floor(before.left + window.scrollX);
        const top = Math.floor(before.top + window.scrollY);
        const geometry = {
          left: `${left}px`, top: `${top}px`,
          width: `${Math.ceil(before.right + window.scrollX) - left}px`,
          height: `${Math.ceil(before.bottom + window.scrollY) - top}px`,
        };
        if (geometry.width === "0px" || geometry.height === "0px") {
          fail("surface has no measurable box");
        }
        publish(geometry);
        const after = surface.getBoundingClientRect();
        if (["x", "y", "width", "height"].some(key => after[key] !== before[key])) {
          fail("surface geometry changed after framing");
        }
      } catch (err) {
        retract(err);
        throw err;
      }
    }

    function track() {
      update();
      requestAnimationFrame(track);
    }
    track();
  }

  function start() {
    const mo = new MutationObserver(() => {
      if (ready()) {
        mo.disconnect();
        begin();
      }
    });
    if (ready()) {
      mo.disconnect();
      begin();
    } else {
      mo.observe(document.documentElement, {
        childList: true,
        subtree: true,
      });
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start, { once: true });
  } else {
    start();
  }
})();
