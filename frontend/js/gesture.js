/* There is no button. The receipt is picked up by its frame and pulled up.
   Past a line near the top of the window it is a print job and flies off;
   short of it, it springs back. A quick flick counts too, since its momentum
   would have carried it there. */

const PRINT_LINE = 0.3;       // this much of the receipt above the window's top
const EDGE = 24;              // px from the window's top where a release is a push through
const FLICK = -900;           // px/s upward that counts as a throw
const MOMENTUM = 0.22;        // seconds of travel credited to a throw
const STIFFNESS = 260;        // the spring home, and the spring down from above
const DAMPING = 24;           // under critical (32), so it lands with a small bounce
const THRUST = 6000;          // px/s^2 upward once it is on its way out
const RUBBER = 28;            // px it can be pulled down before it stops giving
const LAUNCH = -1400;         // px/s, at least, as it leaves

/* Makes `roll` draggable. canGrab(event) says whether a press may pick it
   up; onThrow() runs when a release sends it, and must call fly() or
   settle(). */
export function sheet(roll, { canGrab, onThrow }) {
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

  let y = 0, v = 0;             // offset from rest (px, up is negative) and velocity (px/s)
  let phase = 'rest';           // rest | drag | settle | fly | gone | arrive
  let frame = null, lastT = 0;
  let flown = null;             // resolves once the receipt has left the window
  let drag = null;

  const place = () => { roll.style.transform = y ? `translate3d(0, ${y}px, 0)` : ''; };

  /* One integrator for every motion, semi-implicit Euler at the frame rate.
     Motion is defined by state, not by named animations, so a release at any
     point continues from exactly where the hand left it. */
  function loop(t) {
    frame = null;
    const dt = Math.min((t - lastT) / 1000, 0.032);
    lastT = t;

    if (phase === 'settle' || phase === 'arrive') {
      v += (-STIFFNESS * y - DAMPING * v) * dt;
      y += v * dt;
      if (reduced.matches || (Math.abs(y) < 0.5 && Math.abs(v) < 8)) { y = 0; v = 0; phase = 'rest'; }
    } else if (phase === 'fly') {
      v -= THRUST * dt;
      y += v * dt;
      if (reduced.matches) y = -innerHeight * 2;
      if (roll.getBoundingClientRect().bottom < -8) { phase = 'gone'; flown?.(); flown = null; }
    }

    place();
    if (phase !== 'rest' && phase !== 'gone') schedule();
  }

  function schedule() {
    if (frame !== null) return;
    lastT = performance.now();
    frame = requestAnimationFrame(loop);
  }

  roll.addEventListener('pointerdown', (e) => {
    if (e.button !== 0 || !canGrab(e)) return;
    if (phase === 'fly' || phase === 'gone' || phase === 'arrive') return;

    /* No default: the press must not start a text selection, and must not
       take focus from the writing area. */
    e.preventDefault();
    roll.setPointerCapture(e.pointerId);
    if (frame !== null) { cancelAnimationFrame(frame); frame = null; }

    drag = { id: e.pointerId, y0: y, startY: e.clientY, lastY: y, lastT: e.timeStamp };
    phase = 'drag';
    v = 0;
    roll.classList.add('dragging');
  });

  roll.addEventListener('pointermove', (e) => {
    if (!drag || e.pointerId !== drag.id) return;

    const raw = drag.y0 + (e.clientY - drag.startY);
    /* Up follows the hand. Down gives a little, then stops: there is nowhere
       to go that way, and the resistance says so. */
    y = raw < 0 ? raw : RUBBER * (1 - Math.exp(-raw / (RUBBER * 2)));

    const dt = (e.timeStamp - drag.lastT) / 1000;
    if (dt > 0) v = 0.5 * v + 0.5 * (y - drag.lastY) / dt;
    drag.lastY = y;
    drag.lastT = e.timeStamp;

    place();
  });

  function release(e) {
    if (!drag || e.pointerId !== drag.id) return;

    /* A hand that paused before letting go has no momentum, whatever the
       last move said. */
    if (e.timeStamp - drag.lastT > 90) v = 0;
    drag = null;
    roll.classList.remove('dragging');

    const r = roll.getBoundingClientRect();
    const line = -PRINT_LINE * r.height;
    const thrown = v < FLICK && r.top + v * MOMENTUM < line;
    /* Held by its top edge, the receipt cannot get far out before the hand
       runs into the top of the window. Letting go there is pushing it
       through, whatever fraction has crossed. */
    const pushed = e.type !== 'pointercancel' && e.clientY < EDGE;

    if (r.top < line || thrown || pushed) onThrow();
    else settle();
  }

  roll.addEventListener('pointerup', release);
  roll.addEventListener('pointercancel', release);
  /* A capture lost without an up -- the browser took the pointer for
     itself -- ends the drag the same way. */
  roll.addEventListener('lostpointercapture', (e) => {
    /* Not a spread: an event's fields are prototype getters and would be lost. */
    if (drag) release({ pointerId: e.pointerId, timeStamp: e.timeStamp, type: 'pointercancel', clientY: Infinity });
  });

  /* Spring back to rest from wherever it is. */
  function settle() {
    phase = 'settle';
    schedule();
  }

  /* Send it off the top. Resolves once it has left the window. */
  function fly() {
    phase = 'fly';
    v = Math.min(v, LAUNCH);
    const gone = new Promise(resolve => { flown = resolve; });
    schedule();
    return gone;
  }

  /* A sheet drops in from above the window and springs to rest. */
  function arrive() {
    const r = roll.getBoundingClientRect();
    const restTop = r.top - y;
    y = -(restTop + r.height + 24);
    v = 0;
    phase = 'arrive';
    place();
    schedule();
  }

  return {
    get resting() { return phase === 'rest'; },
    settle, fly, arrive,
  };
}
