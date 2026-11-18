import './style.css';

const ZONE = 'Asia/Kolkata';
const RESOURCE = 'room-1';
const STORE_KEY = 'loop.reservations.v1';
const users = [{ id: 'user-1', name: 'Alice' }, { id: 'user-2', name: 'Bob' }, { id: 'user-3', name: 'Charlie' }];
const timeFormat = new Intl.DateTimeFormat('en', { timeZone: ZONE, hour: 'numeric', minute: '2-digit' });
const dateFormat = new Intl.DateTimeFormat('en', { timeZone: ZONE, month: 'short', day: 'numeric' });
const dateKey = (value) => new Intl.DateTimeFormat('en-CA', { timeZone: ZONE, year: 'numeric', month: '2-digit', day: '2-digit' }).format(value);
const today = dateKey(new Date());
const tomorrow = dateKey(new Date(Date.now() + 86400000));
let memory = { holds: [], bookings: [] };
const state = { user: sessionStorage.getItem('loop.user') || 'user-1', date: tomorrow, slots: [], loading: true, busy: false, lastBooking: null };
let requestVersion = 0;
let slotSignature = '';
let expiredNotice = '';
let checkoutSignature = '';
let bookingsSignature = '';
const $ = (selector) => document.querySelector(selector);
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
const when = (slot) => `${timeFormat.format(new Date(slot.startTime))} – ${timeFormat.format(new Date(slot.endTime))}`;
const niceDate = (date) => new Intl.DateTimeFormat('en', { timeZone: ZONE, weekday: 'long', month: 'long', day: 'numeric' }).format(new Date(`${date}T12:00:00+05:30`));

function records() {
  try {
    const value = JSON.parse(localStorage.getItem(STORE_KEY));
    if (Array.isArray(value?.holds) && Array.isArray(value?.bookings)) memory = value;
  } catch { /* Keep the current session usable if browser storage is unavailable. */ }
  return memory;
}
function save(type, record, remove = false) {
  const current = records();
  current[type] = current[type].filter((item) => item.id !== record.id);
  if (!remove) current[type].push(record);
  memory = current;
  try { localStorage.setItem(STORE_KEY, JSON.stringify(current)); } catch { /* Session still works in memory. */ }
}
const secondsLeft = (hold) => Math.max(0, Math.ceil((Date.parse(hold.expiresAt) - Date.now() - (hold.clockOffset || 0)) / 1000));
const currentHold = () => records().holds.filter((h) => h.userId === state.user).sort((a, b) => b.createdAt - a.createdAt)[0];

$('#app').innerHTML = `
  <header class="topbar"><a class="brand" href="/" aria-label="Loop home"><span class="brand-mark">L<span>↗</span></span>loop<span class="brand-sub">/ reservations</span></a><span class="demo-label"><span class="dot"></span> Demo workspace</span></header>
  <main>
    <section class="intro"><div><p class="eyebrow">A SPACE FOR WHAT'S NEXT</p><h1>Make room for<br><em>your next idea.</em></h1><p class="intro-copy">Find a time, hold your spot, and make it yours.</p></div>
      <div class="user-control"><label for="user-select">BOOKING AS</label><div class="user-select-wrap"><span class="avatar" id="avatar">A</span><select id="user-select" aria-label="Demo user">${users.map((u) => `<option value="${u.id}">${u.name} · ${u.id}</option>`).join('')}</select></div><p>Open another tab with a different user<br>to try booking the same slot.</p></div>
    </section>
    <div id="notice" class="notice" role="status" aria-live="polite" hidden></div>
    <div class="workspace">
      <section class="schedule panel" aria-labelledby="schedule-title">
        <div class="room-heading"><div class="room-icon" aria-hidden="true">↗</div><div><p class="eyebrow">MEETING ROOM</p><h2 id="schedule-title">The room for your team.</h2><p class="room-details">Meeting Room A <span>·</span> 30-minute sessions <span>·</span> IST</p></div></div>
        <div class="date-toolbar"><div class="date-shortcuts"><button id="today" class="date-chip">Today</button><button id="tomorrow" class="date-chip">Tomorrow</button></div><label class="date-picker"><span class="sr-only">Choose date</span><input id="date" type="date" min="${today}" value="${tomorrow}" /></label></div>
        <div class="day-heading"><div><h3 id="day-title"></h3><p id="availability-count">Loading availability…</p></div><button class="text-button" id="refresh" aria-label="Refresh availability">↻ <span>Refresh</span></button></div>
        <div class="legend"><span><i class="legend-dot available"></i> Available</span><span><i class="legend-dot held"></i> Held</span><span><i class="legend-dot booked"></i> Booked</span></div>
        <div id="slots" class="slot-grid" aria-live="polite"></div>
        <div class="schedule-footer"><span class="dot"></span><span id="sync-status">Checking availability</span><span class="footer-zone">All times in India Standard Time</span></div>
      </section>
      <aside class="checkout panel" aria-labelledby="checkout-title"><div class="checkout-label"><span class="step-number">02</span><span>YOUR RESERVATION</span></div><div id="checkout"></div></aside>
    </div>
    <section class="bookings-section" aria-labelledby="bookings-title"><div class="bookings-heading"><div><p class="eyebrow">YOUR PLANS, IN ONE PLACE</p><h2 id="bookings-title">My reservations</h2></div><span id="booking-count" class="count-pill">0 upcoming</span></div><div id="bookings"></div></section>
    <footer class="page-footer"><span>loop <span class="footer-slash">/</span> A little room goes a long way.</span><span>30-second holds. No double bookings.</span></footer>
  </main>`;

async function api(path, options = {}) {
  let response;
  try {
    response = await fetch(`/api${path}`, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers }, signal: AbortSignal.timeout(10000) });
  } catch { throw new Error('Could not reach the booking service. Please try again.'); }
  if (response.status === 204) return null;
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    const error = new Error(body?.message || 'The request could not be completed. Please try again.');
    error.code = body?.error;
    throw error;
  }
  return body;
}
function notice(message, error = false) {
  $('#notice').hidden = !message;
  $('#notice').textContent = message;
  $('#notice').className = `notice${error ? ' error' : ''}`;
}
async function refresh(showLoading = false) {
  if (state.busy) return;
  const version = ++requestVersion;
  const date = state.date;
  if (showLoading) { state.loading = true; renderSlots(); }
  try {
    const slots = await api(`/resources/${RESOURCE}/slots?date=${encodeURIComponent(date)}`);
    if (version !== requestVersion) return;
    state.slots = slots;
    $('#sync-status').textContent = 'Live availability · refreshes every 3s';
  } catch (error) {
    if (version !== requestVersion) return;
    notice(error.message, true);
    $('#sync-status').textContent = 'Unable to sync · try Refresh';
  } finally {
    if (version === requestVersion) { state.loading = false; render(); }
  }
}
function renderSlots() {
  $('#day-title').textContent = niceDate(state.date);
  $('#date').value = state.date;
  $('#today').classList.toggle('selected', state.date === today);
  $('#tomorrow').classList.toggle('selected', state.date === tomorrow);
  const active = currentHold();
  const hasHold = active && secondsLeft(active) > 0;
  const signature = JSON.stringify([state.slots, state.loading, state.busy, state.user, hasHold ? active.id : null, state.date]);
  if (signature === slotSignature) return;
  slotSignature = signature;
  const available = state.slots.filter((slot) => slot.status === 'AVAILABLE').length;
  $('#availability-count').textContent = state.loading ? 'Loading availability…' : `${available} of ${state.slots.length} slots available`;
  if (state.loading) { $('#slots').innerHTML = '<p class="empty-state">Finding a little space for you…</p>'; return; }
  if (!state.slots.length) {
    $('#slots').innerHTML = `<div class="empty-state"><span class="empty-icon">◷</span><strong>No slots for this day.</strong><span>Try today or tomorrow for available room hours.</span></div>`;
    return;
  }
  $('#slots').innerHTML = state.slots.map((slot) => {
    const own = hasHold && active.slotId === slot.id;
    const label = own ? 'Your hold' : { AVAILABLE: 'Available', HELD: 'On hold', BOOKED: 'Booked' }[slot.status];
    const disabled = state.busy || (!own && (slot.status !== 'AVAILABLE' || hasHold));
    return `<button class="slot ${slot.status.toLowerCase()} ${own ? 'own' : ''}" data-slot="${escape(slot.id)}" ${disabled ? 'disabled' : ''} aria-label="${escape(when(slot))}, ${label}"><span class="slot-time">${escape(timeFormat.format(new Date(slot.startTime)))}</span><span class="slot-end">to ${escape(timeFormat.format(new Date(slot.endTime)))}</span><span class="slot-status"><i></i>${label}</span>${own ? '<span class="slot-check">✓</span>' : ''}</button>`;
  }).join('');
}
function renderCheckout() {
  const hold = currentHold();
  const booked = state.lastBooking && records().bookings.find((b) => b.id === state.lastBooking && b.status === 'CONFIRMED' && b.userId === state.user);
  const signature = JSON.stringify([hold, booked, state.busy, state.user, hold ? secondsLeft(hold) === 0 : false]);
  if (signature === checkoutSignature) return;
  checkoutSignature = signature;
  if (booked) {
    $('#checkout').innerHTML = `<div class="success-icon">✓</div><h2 id="checkout-title">It's on the calendar.</h2><p class="muted">Your room is booked. A little space to do your best work.</p>${reservationDetails(booked)}<button class="button secondary wide" id="book-another">Book another slot</button>`;
    return;
  }
  if (!hold) {
    $('#checkout').innerHTML = `<div class="checkout-illustration" aria-hidden="true"><div class="mini-calendar"><span>YOUR NEXT IDEA</span><div><i></i><i></i><i></i><i></i><i></i><i></i><i></i><i class="mini-selected">↗</i><i></i></div></div></div><h2 id="checkout-title">Your time starts here.</h2><p class="muted">Pick an available slot to get started. We'll keep it just for you while you confirm.</p><div class="checkout-note"><span>◷</span><div><strong>30 seconds, just for you.</strong><p>Your hold expires automatically if you leave checkout.</p></div></div>`;
    return;
  }
  const expired = secondsLeft(hold) === 0;
  $('#checkout').innerHTML = `<h2 id="checkout-title">${expired ? "Time's up." : 'A moment, reserved.'}</h2><p class="muted">${expired ? 'Your hold has expired. Choose an available time to start again.' : 'Your slot is held exclusively for you. Confirm before the timer runs out.'}</p>${reservationDetails(hold)}<div class="timer-box ${expired ? 'expired' : ''}"><div><span>${expired ? 'HOLD EXPIRED' : 'TIME TO CONFIRM'}</span><strong id="countdown" role="timer" aria-live="off">${secondsLeft(hold)}<small>s</small></strong></div><div class="timer-track"><div id="timer-progress"></div></div></div>${expired ? '<button class="button secondary wide" id="dismiss-hold">Choose another time</button>' : `<button class="button primary wide" id="confirm" ${state.busy ? 'disabled' : ''}>${state.busy ? 'One moment…' : 'Confirm reservation'} <span>↗</span></button><button class="text-button release-button" id="release" ${state.busy ? 'disabled' : ''}>Release this slot</button>`}<p class="checkout-caption">No payment needed for this demo.</p>`;
  updateTimer();
}
function reservationDetails(record) {
  return `<div class="reservation-details"><div><span>SPACE</span><strong>Meeting Room A</strong></div><div><span>WHEN</span><strong>${escape(dateFormat.format(new Date(record.startTime)))}</strong><p>${escape(when(record))} IST</p></div><div><span>BOOKING FOR</span><strong>${escape(users.find((u) => u.id === record.userId)?.name || record.userId)}</strong></div></div>`;
}
function renderBookings() {
  const bookings = records().bookings.filter((b) => b.userId === state.user).sort((a, b) => b.createdAt - a.createdAt);
  const signature = JSON.stringify([bookings, state.busy, state.user]);
  if (signature === bookingsSignature) return;
  bookingsSignature = signature;
  const count = bookings.filter((b) => b.status === 'CONFIRMED').length;
  $('#booking-count').textContent = `${count} confirmed`;
  $('#bookings').innerHTML = bookings.length ? bookings.map((booking) => `<article class="booking-row"><div class="booking-date"><strong>${escape(dateFormat.format(new Date(booking.startTime)))}</strong><span>MEETING ROOM</span></div><div class="booking-info"><h3>Meeting Room A</h3><p>${escape(when(booking))} IST</p></div><span class="booking-badge ${booking.status === 'CANCELLED' ? 'cancelled' : ''}">${booking.status === 'CANCELLED' ? 'Cancelled' : 'Confirmed'}</span>${booking.status === 'CONFIRMED' ? `<button class="text-button cancel-button" data-cancel="${escape(booking.id)}" ${state.busy ? 'disabled' : ''}>Cancel booking</button>` : '<span class="cancelled-dash">—</span>'}</article>`).join('') : '<div class="bookings-empty"><span>↗</span><div><strong>A blank calendar, full of possibility.</strong><p>Your confirmed reservations will appear here.</p></div></div>';
}
function render() {
  $('#user-select').value = state.user;
  $('#user-select').disabled = state.busy;
  $('#avatar').textContent = users.find((u) => u.id === state.user)?.name[0] || '?';
  $('#date').disabled = state.busy;
  $('#today').disabled = state.busy;
  $('#tomorrow').disabled = state.busy;
  $('#refresh').disabled = state.busy || state.loading;
  renderSlots(); renderCheckout(); renderBookings();
}
function updateTimer() {
  const hold = currentHold();
  if (!hold || !$('#countdown')) return;
  const seconds = secondsLeft(hold);
  $('#countdown').innerHTML = `${seconds}<small>s</small>`;
  if ($('#timer-progress')) $('#timer-progress').style.width = `${Math.min(100, seconds / 30 * 100)}%`;
  if (seconds === 0 && expiredNotice !== hold.id) {
    expiredNotice = hold.id;
    renderSlots(); renderCheckout();
    refresh();
  }
}
async function action(operation) {
  if (state.busy) return;
  state.busy = true; ++requestVersion; state.loading = false;
  notice(''); render();
  try { await operation(); }
  catch (error) { notice(error.message, true); }
  finally { state.busy = false; render(); await refresh(); }
}
function setDate(date) {
  if (!date || state.busy) return;
  state.date = date; state.slots = [];
  notice(''); refresh(true);
}
$('#user-select').addEventListener('change', (event) => {
  state.user = event.target.value;
  sessionStorage.setItem('loop.user', state.user);
  state.lastBooking = null;
  notice(''); render(); refresh();
});
$('#date').addEventListener('change', (event) => setDate(event.target.value));
$('#today').addEventListener('click', () => setDate(today));
$('#tomorrow').addEventListener('click', () => setDate(tomorrow));
$('#refresh').addEventListener('click', () => { notice(''); refresh(true); });
$('#slots').addEventListener('click', (event) => {
  const button = event.target.closest('[data-slot]');
  if (!button || button.disabled) return;
  const current = currentHold();
  if (current && secondsLeft(current) > 0) { $('#confirm')?.focus(); return; }
  const slot = state.slots.find((s) => s.id === button.dataset.slot);
  if (!slot) return;
  action(async () => {
    const hold = await api(`/slots/${encodeURIComponent(slot.id)}/hold`, { method: 'POST', body: JSON.stringify({ userId: state.user }) });
    records().holds.filter((h) => h.userId === state.user).forEach((h) => save('holds', h, true));
    save('holds', { ...hold, startTime: slot.startTime, endTime: slot.endTime, createdAt: Date.now(), clockOffset: Date.parse(hold.serverTime) - Date.now() });
    state.lastBooking = null;
    notice('Your slot is held for 30 seconds. Confirm when you’re ready.');
    renderCheckout();
    if (window.innerWidth <= 740) $('#checkout').scrollIntoView({ block: 'start', behavior: 'smooth' });
  });
});
$('#checkout').addEventListener('click', (event) => {
  const target = event.target.closest('button');
  if (!target || target.disabled) return;
  if (target.id === 'book-another') { state.lastBooking = null; renderCheckout(); return; }
  const hold = currentHold();
  if (!hold) return;
  if (target.id === 'dismiss-hold') { save('holds', hold, true); render(); refresh(); return; }
  if (target.id === 'confirm') action(async () => {
    const booking = await api(`/holds/${encodeURIComponent(hold.id)}/confirm`, { method: 'POST', body: JSON.stringify({ userId: state.user }) });
    save('bookings', { ...booking, startTime: hold.startTime, endTime: hold.endTime, createdAt: Date.now() });
    save('holds', hold, true); state.lastBooking = booking.id;
    notice('Reservation confirmed. Your room is ready when you are.');
  });
  if (target.id === 'release') action(async () => {
    await api(`/holds/${encodeURIComponent(hold.id)}`, { method: 'DELETE', body: JSON.stringify({ userId: state.user }) });
    save('holds', hold, true); notice('Slot released. You can choose another time.');
  });
});
$('#bookings').addEventListener('click', (event) => {
  const button = event.target.closest('[data-cancel]');
  if (!button || button.disabled) return;
  const booking = records().bookings.find((b) => b.id === button.dataset.cancel && b.userId === state.user);
  if (!booking) return;
  action(async () => {
    await api(`/bookings/${encodeURIComponent(booking.id)}`, { method: 'DELETE', body: JSON.stringify({ userId: state.user }) });
    save('bookings', { ...booking, status: 'CANCELLED' });
    notice('Reservation cancelled. The slot is available again.');
  });
});
window.addEventListener('storage', (event) => { if (event.key === STORE_KEY) { render(); refresh(); } });
window.addEventListener('focus', () => refresh());
setInterval(() => { if (!document.hidden) refresh(); }, 3000);
setInterval(updateTimer, 250);
render(); refresh(true);
