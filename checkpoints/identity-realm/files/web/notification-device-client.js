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
