---
title: Event
type: schema
entity: event
version: 1
schema:
  date: string, ISO date or date-time; as precise as known (2027-06, 2027-06-14, 2027-06-14T18:30)
  kind?(enum): [appointment, travel, stay, ticket, meeting, delivery, renewal, milestone, other]
  end?: string, ISO date or date-time when it ends
  recurring?: string, how often it repeats, in words
  with?(array): Person, who is involved
  at?: Place, where it happens
  confirmation?: string, booking reference, order number or ticket number
  link?(array): string, related URLs such as the booking page or parcel tracking
  checklist?(array): string, one thing to bring or do beforehand per line
  source?: string, where the event came from when not this conversation, such as the mail it was taken from
settings:
  validation: warn
---

Something that happened or will happen at a time. Its kind is appointment
(a doctor, dentist or other visit), travel (a flight, train or other
journey), stay (a hotel or rental), ticket (a show, match or table
reservation), meeting, delivery (an order arriving, dated by its expected
delivery), renewal (a subscription or policy that renews and charges on its
own), milestone or other. Save in folder events/<YYYY>/, the year of its
date, with note_type event, titled with what it is, naming the airline,
hotel, shop or service for bookings and orders.

Link the place with at, which is also where a clinic, hotel or venue goes,
and the people involved with with. When it moves, update its date rather
than writing a second note.

For travel, stays and appointments, checklist holds what to bring or prepare.
Start it from the user's preferences for that kind of event, then add what
this one needs. When the user ticks items off, remove them from the list.
