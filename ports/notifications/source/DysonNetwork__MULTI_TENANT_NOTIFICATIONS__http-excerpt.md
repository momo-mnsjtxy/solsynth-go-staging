### Notification Endpoints

All notification listing/counting endpoints accept `?app=`:

#### Count Unread

```
GET /api/notifications/count?app=dev.solsynth.solian
```

#### List Notifications

```
GET /api/notifications?app=dev.solsynth.solian&offset=0&take=8
```

#### Mark All Read

```
POST /api/notifications/all/read?app=dev.solsynth.solian
```

#### Send Notification

```
POST /api/notifications/send?app=dev.solsynth.solian
```

The `app` param is stored on saved notifications and used for delivery routing.

**Request Body:**

| Field | Type | Description |
|-------|------|-------------|
| `push_type` | string? | APNS topic type: `"Alert"` (default), `"VoIP"`, etc. Maps to the `Topics` dictionary in config. |


