---
title: Event
type: schema
entity: event
version: 1
schema:
  date: string, ISO date or date-time; as precise as known (2027-06, 2027-06-14, 2027-06-14T18:30)
  kind?(enum): [appointment, travel, stay, ticket, meeting, delivery, milestone, other]
  end?: string, ISO date or date-time when it ends
  recurring?: string, how often it repeats, in words
  with?(array): Person, who is involved
  at?: Place, where it happens
  confirmation?: string, booking reference, order number or ticket number
  link?(array): string, related URLs such as the booking page or tracking
  checklist?(array): string, one thing to bring or do beforehand per line
settings:
  validation: warn
---

Something that happened or will happen at a time. Its kind is appointment
(a visit to a professional), travel (a journey), stay (lodging), ticket (an
admission or reservation), meeting, delivery (an order arriving, dated by
its expected delivery), milestone or other. Save in folder events/<YYYY>/,
the year of its date, with note_type event, titled with what it is, naming
the provider for bookings and orders.

Link the venue with at and the people involved with with. When it moves,
update its date rather than writing a second note, and move it with
move_note when its year changes.

The note has no status. While the event is still ahead, track it with the
items tools: save_item when it is written or its date moves, and close_item when it is cancelled, or done
when a delivery arrives.

Checklist holds what to bring or prepare. Start it from the user's
preferences for that kind of event, then add what this one needs. When the
user ticks items off, remove them from the list.
