// SPDX-License-Identifier: AGPL-3.0-only
// Connect the application's configured Firebase onMessage/getToken adapters.
// Access credentials remain in the owner's authenticated session, never URLs.
export async function registerNotificationDevice({ getToken, getAccessToken, onMessage, deviceID, label = '', consentHours = 24 }) {
  const buffered = new Map();
  const listeners = new Map();
  const off = onMessage(payload => {
    const data = payload?.data;
    if (data?.kind !== 'device.challenge' || !data.challenge_id || !data.challenge) return;
    const accept = listeners.get(data.challenge_id);
    if (accept) accept(data.challenge);
    else { if (buffered.size >= 8) buffered.delete(buffered.keys().next().value); buffered.set(data.challenge_id, data.challenge); }
  });
  async function call(path, body) {
    const response = await fetch(path, { method: 'POST', headers: { 'Authorization': `Bearer ${await getAccessToken()}`, 'Content-Type': 'application/json' }, cache: 'no-store', body: JSON.stringify(body) });
    if (!response.ok) throw new Error('Notification device registration failed.');
    return response.json();
  }
  try {
    const start = await call('/api/notifications/devices', { device_id: deviceID, label, token: await getToken(), consent_hours: consentHours });
    const code = buffered.get(start.challenge_id) ?? await new Promise((resolve, reject) => {
      const timer = setTimeout(() => { listeners.delete(start.challenge_id); reject(new Error('Device confirmation expired.')); }, Math.min(60000, Math.max(0, Date.parse(start.expires_at) - Date.now())));
      listeners.set(start.challenge_id, secret => { clearTimeout(timer); listeners.delete(start.challenge_id); resolve(secret); });
    });
    return await call('/api/notifications/devices/confirm', { challenge_id: start.challenge_id, code });
  } finally { if (typeof off === 'function') off(); buffered.clear(); listeners.clear(); }
}

export async function revokeNotificationDevice(deviceID, getAccessToken) {
  const response = await fetch(`/api/notifications/devices/${encodeURIComponent(deviceID)}`, { method: 'DELETE', headers: { 'Authorization': `Bearer ${await getAccessToken()}` }, cache: 'no-store' });
  if (!response.ok) throw new Error('Notification device revocation failed.');
}

// FCM acceptance is not exactly-once delivery. Deduplicate the stable public
// wake-up key durably across restarts/tabs, then fetch the current canonical
// inbox with the client's authenticated refreshInbox implementation. Payloads
// never supply account identity or private display content.
export async function observeNotificationWakeups({ onMessage, getAccountID, appID, refreshInbox, onUnavailable }) {
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
  if (typeof onMessage !== 'function' || typeof getAccountID !== 'function' || typeof refreshInbox !== 'function' || !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(appID)) throw new Error('Notification receiver configuration unavailable.');
  const db = await new Promise((resolve, reject) => {
    const request = indexedDB.open('solsynth-notification-wakeups', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('receipts', { keyPath: 'key' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(new Error('Durable notification receipts unavailable.'));
  });
  let stopped = false;
  async function receive(payload) {
    const data = payload?.data;
    if (stopped || data?.kind !== 'notifications.wakeup' || data.app_id !== appID || !uuid.test(data.notification_id) || data.delivery_key !== data.notification_id) return;
    const owner = await getAccountID();
    if (stopped || !uuid.test(owner)) return;
    const key = `${owner}:${appID}:${data.delivery_key}`;
    const claimed = await new Promise((resolve, reject) => {
      const tx = db.transaction('receipts', 'readwrite');
      let duplicate = false;
      const request = tx.objectStore('receipts').add({ key });
      request.onerror = event => {
        if (request.error?.name === 'ConstraintError') { duplicate = true; event.preventDefault(); event.stopPropagation(); }
      };
      tx.oncomplete = () => resolve(!duplicate);
      tx.onabort = () => reject(new Error('Durable notification receipts unavailable.'));
    });
    if (!claimed || stopped || await getAccountID() !== owner) return;
    await refreshInbox();
  }
  const off = onMessage(payload => { void receive(payload).catch(() => { if (typeof onUnavailable === 'function') onUnavailable(); }); });
  return () => { stopped = true; if (typeof off === 'function') off(); db.close(); };
}
