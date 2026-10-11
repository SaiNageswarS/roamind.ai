---
title: Event
type: schema
entity: event
version: 1
schema:
  date: string, ISO date or date-time; as precise as known (2027-06, 2027-06-14, 2027-06-14T18:30)
  recurring?: string, e.g. yearly, every Monday
  with?(array): Person, who is involved
  at?: Place, where it happens
settings:
  validation: warn
---

Something that happened or will happen: trips, appointments, milestones.
Save in folder events/ with note_type event.
