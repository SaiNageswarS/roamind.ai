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
  location?(array): string, where they live
  skill?(array): string, skills and tools, grouped as on a resume
  project?(array): 'string, one per line: <name>: <what it is and what came of it>'
  publication?(array): 'string, one per line: <title>, <venue> <YYYY>'
settings:
  validation: warn
---

Someone in the user's life, including the user themself (relationship self,
exactly one such note). Save in folder people/ with note_type person.

Experience, education, skill, project, publication and interest capture
someone's career and research; on the user's own note they are their
resume, which the job assistant writes applications from. A resume or CV
they share is the latest word on their career: where it lists roles,
education, skills, projects or publications, those replace the note's;
contact details and locations it gives are added to the note's; every
other line stays.
Each role or degree is one line in the format given by its field, so its
parts stay together; the current role ends with "to present".

Workplaces live in Place notes, linked with works_at relations that point at
the place's permalink.
