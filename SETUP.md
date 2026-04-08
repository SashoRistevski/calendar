# iCloud CalDAV setup

This service reads one iCloud calendar with an **App-Specific Password** (never your Apple ID password).

## 1. Generate an App-Specific Password

1. Sign in at [https://appleid.apple.com/](https://appleid.apple.com/).
2. Open **Sign-In and Security** (or **Security** on older layouts).
3. Under **App-Specific Passwords**, choose **Generate an app-specific password…**.
4. Label it (for example, `CalDAV availability proxy`) and copy the password.  
   Store it as `ICLOUD_APP_PASSWORD` on Render (or in a local `.env` for development). You cannot view it again after dismissing the dialog.

## 2. Principal URL (iCloud)

For personal iCloud calendars the CalDAV entry point is:

`https://caldav.icloud.com`

The server discovers your **current-user-principal** and **calendar-home-set** automatically. You do not need to paste a principal URL into this app unless you use a non-default host (override with `CALDAV_BASE_URL`).

## 3. List calendar IDs (paths)

Calendar **paths** look like:

`/1234567890/calendars/XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX/`

Use the helper command to print **Path** and **Name** for every calendar on your account:

```bash
set ICLOUD_EMAIL=you@icloud.com
set ICLOUD_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx
go run ./cmd/list-calendars
```

On macOS/Linux:

```bash
export ICLOUD_EMAIL='you@icloud.com'
export ICLOUD_APP_PASSWORD='xxxx-xxxx-xxxx-xxxx'
go run ./cmd/list-calendars
```

Copy the **Path** value for the calendar you want and set it as `CALENDAR_ID`.

**Tips:**

- If the path already contains `/calendars/`, you can paste it verbatim as `CALENDAR_ID`.
- If you only have the UUID folder name, you can set `CALENDAR_ID` to that UUID; the server resolves it under your calendar home.

## 4. CalDAV connectivity check (optional)

With the server running, set **`CALDAV_DIAGNOSTICS=1`** (local debugging only — **do not** turn this on for a public URL). Then open:

`GET /api/diagnostics/caldav`

The JSON walks through: principal discovery, calendar home, `PROPFIND` listing, `calendar-query` with and without expand, and a `GET` on the first calendar resource. It does **not** return event titles or descriptions. Check **`availability_preview`**: `slots_floating_as_utc` vs `slots_floating_as_window_tz` shows how many slots survive parsing for each floating-time assumption (UTC matches [rehearsal-calculator](https://github.com/SashoRistevski/rehearsal-calculator)).

Example (Docker):

```bash
docker run --rm -p 8080:8080 \
  -e CALDAV_DIAGNOSTICS=1 \
  -e ICLOUD_EMAIL='…' -e ICLOUD_APP_PASSWORD='…' -e CALENDAR_ID='…/' \
  -e PORT=8080 \
  calendar-proxy:local
```

Then visit `http://localhost:8080/api/diagnostics/caldav`.

## 5. Local run

```bash
export ICLOUD_EMAIL='you@icloud.com'
export ICLOUD_APP_PASSWORD='xxxx-xxxx-xxxx-xxxx'
export CALENDAR_ID='/…/calendars/…/'
go run ./cmd/server
```

Open [http://localhost:8080/](http://localhost:8080/). The UI calls `GET /api/availability`, which returns **only** JSON objects with `start` and `end` (RFC3339 in `Europe/Skopje`). The **Refresh now** control and periodic auto-sync use `GET /api/availability?fresh=1`, which **skips the server cache** and pulls from iCloud again (still rate-limited). Recurring events (`RRULE`) are **expanded in the query window** on the server so every occurrence appears as its own interval.

The API window is **Monday 00:00 (Europe/Skopje) of the current ISO week** through **31 days after that Monday**. Responses omit intervals that have **already ended** (ongoing events still appear). The UI week view starts on **Monday** and cannot scroll before that window.

## 6. Render (free tier)

1. Push this repository to GitHub (or connect your Git provider to Render).
2. **New** → **Web Service** → choose the repo → **Docker** as environment.
3. Set environment variables:
   - `ICLOUD_EMAIL`
   - `ICLOUD_APP_PASSWORD`
   - `CALENDAR_ID`
4. Optional: `CACHE_TTL_SECONDS` (default **90** without env — balances iCloud load vs freshness), `RATE_PER_SECOND`, `RATE_BURST`, `CALDAV_BASE_URL`.
5. **ICS floating times:** default is **`EVENT_PARSE_TIMEZONE` unset → parse naive `DTSTART`/`DTEND` as UTC**, matching [rehearsal-calculator `to_utc`](https://github.com/SashoRistevski/rehearsal-calculator/blob/main/calendar-calc.py). If slots look shifted, try `EVENT_PARSE_TIMEZONE=Europe/Skopje`.
6. **`SKIP_TRANSPARENT`:** set to `true` only if you want to hide `TRANSP:TRANSPARENT` events (default **off**, same as the Python script).
7. Render sets `PORT` automatically; the server listens on `$PORT`.

You can also use the included `render.yaml` with **Blueprint** if you prefer infrastructure-as-code. Mark secret values as **Secret** in the Render dashboard for credentials.

## Troubleshooting `403 Forbidden` on diagnostics / startup

1. **App-specific password** — `ICLOUD_APP_PASSWORD` must be a **16-character app password** from [appleid.apple.com](https://appleid.apple.com/) → App-Specific Passwords. Your normal Apple ID password will not work.
2. **Apple ID email** — `ICLOUD_EMAIL` must be the **full** Apple ID (often the same as your iCloud email). No extra spaces in Docker `-e` values.
3. **Regenerate** — Revoke the old app password and create a new one if anything might be wrong; update the env var.
4. **iCloud terms / account** — Sign in to iCloud once on a device or [icloud.com](https://www.icloud.com) so the account is fully active.
5. **User-Agent** — This app sends a CalDAV-style `User-Agent` (Apple blocks many generic clients with **403**). Use the latest code if you still see 403 after checking credentials.

If diagnostics show **`read_dir` error** mentioning `DAV: resourcetype` / **404**, but **`calendar_query_*`** report several objects — that is normal on iCloud; listing uses a different `PROPFIND` shape than Apple supports well. The app uses **calendar-query REPORT** for data, which is what matters.

## Security notes

- Rotate the app-specific password if it leaks; revoke it from the same Apple ID page.
- The public API never returns titles, locations, descriptions, or attendee data—only busy intervals as `start` / `end`.
