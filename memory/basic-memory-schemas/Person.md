---
title: Person
type: schema
entity: person
version: 1
schema:
  relationship: string, how they relate to the user (colleague, advisor, collaborator, friend), or self for the user themself
  contact?(array): string, phone, email or handle
  works_at?(array): Place, where they work
  experience?(array): 'string, one role per line: <role>, <organization>, <YYYY-MM> to <YYYY-MM or present>, optionally followed by a colon and highlights'
  education?(array): 'string, one per line: <qualification>, <institution>, <YYYY> to <YYYY>'
  interest?(array): string, topics they work on or care about
settings:
  validation: warn
---

Someone in the user's life, including the user themself (relationship self,
exactly one such note). Save in folder people/ with note_type person.

Experience, education and interest capture someone's career and research.
Each role or degree is one line in the format given by its field, so its
parts stay together; the current role ends with "to present".

Workplaces live in Place notes, linked with works_at relations that point at
the place's permalink.
