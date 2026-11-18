<img width="1722" height="1108" alt="Screenshot 2026-11-18 at 4 38 55 PM" src="https://github.com/user-attachments/assets/33aeacd5-5417-41da-8151-f824013e98b1" />


We’re building a generic slot booking platform.
Think of something similar to:
Doctor appointments (doctor, patient)
Meeting room reservations (invitees)
Restaurant table booking (customers)
Sports turf reservations (athletes)
Delivery time slots (delivery person, users)
Every slot belongs to one resource and represents a time window.
Users should be able to:
View available slots
Start booking a slot
Confirm the booking
Cancel an existing booking
The booking process is not instantaneous.
 A user may spend a minute entering details or making payment before confirming.
During that time, the slot should not be bookable by someone else.
If the user abandons the checkout, the slot should automatically become available again.
Build a small backend and a minimal frontend that demonstrates this flow.
Use any language, framework, or AI tools you like.
Before writing code, spend a few minutes explaining your design.


Requirements:
-> Functional Req:
-> View available slots for a resource
- > temporary hold a slot during checkout
- > confirm a held slot
- cancel a booking
- automatically release expired holds.



One slot belongs to one resource
One active booking per slot
Hold duration: 30 seconds
Predefined: non-overlapping slots per user
Muliple users may attempt to book the same slot concurrently

Core Entities:
Resource ( id, name, type )
Slot (id, resourceId, startTime, endTime, availability_config_id)
Hold (id, slotId, userId, expiresAt, status)
Booking (id, slotId, userId, status)

APIs:
GET /slots?resourceId=1 -> List slots
POST /slots/{id}/hold -> create temporary hold
POST /holds/{id}/confirm -> Confirm Booking
DELETE /holds/{id} -> Release hold
DELETE /bookings/{id} -> Cancel Booking

AvailabilityConfig
Id
resourceId
startTime, end_time
Slot_duration_minutes

/resource/{id}/availability -> Configure Resource avail.
GET /resource/{id}/slots?date=... - Generate possible slots from the config





## Run the demo

Terminal 1:

```sh
cd backend
go run ./cmd/api -seed
```

Terminal 2:

```sh
cd frontend
npm install
npm run dev
```

Open http://127.0.0.1:5173. Use different demo users in separate tabs to try
concurrent holds. See [backend instructions](backend/README.md) and
[frontend instructions](frontend/README.md) for API details and demo behavior.
The background cleanup worker is deferred; timestamp checks already enforce
hold expiry immediately.
