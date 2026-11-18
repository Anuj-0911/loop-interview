# Loop frontend

A Vite + vanilla JavaScript frontend for the Go/SQLite booking backend.

## Run

Start the API in one terminal:

```sh
cd backend
go run ./cmd/api -seed
```

Start the frontend in another terminal from the repository root:

```sh
cd frontend
npm install
npm run dev
```

Open http://127.0.0.1:5173. Node.js 22.12+ is required (Node 24 is supported).
Vite proxies `/api` requests to the API at `127.0.0.1:8080`; no CORS changes are
needed. If the backend is already running with demo data, reuse it.

## Demo flow

- Select Alice, Bob, or Charlie. The selected user is stored per tab.
- Pick today or tomorrow and hold an available half-hour slot.
- Confirm within 30 seconds, or release the hold. Leaving it alone lets it expire.
- Cancel a confirmed reservation under My reservations.
- Open a second tab and select a different user to test competing bookings.

Availability refreshes every three seconds; the backend is authoritative and
rejects conflicting requests immediately. The checkout countdown uses the
server-provided expiry and clock offset, so it resumes after a page reload.
Expired holds stop blocking slots even though the cleanup worker is deferred.
All room times are shown in Asia/Kolkata, matching the seeded room.

Holds and confirmed bookings created here are remembered in this browser's local
storage. Tabs share reservation updates; their selected users remain independent.
My reservations is a browser-local demo history, not a backend booking-search
endpoint. Reservations created with curl or in another browser will not appear
there. If you replace/reset the database, clear this site's browser storage too.

## Build

```sh
npm run build
npm run preview
```

`preview` serves the build at http://127.0.0.1:4173 and also proxies to the local
API. A deployed build needs a same-origin `/api` reverse proxy to the Go backend;
no hosting deployment is configured.
