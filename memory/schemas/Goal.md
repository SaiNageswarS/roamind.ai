---
title: Goal
type: schema
entity: goal
version: 1
schema:
  status(enum): [active, paused, achieved, dropped]
  outcome: string, what achieving the goal looks like, as concretely as possible
  kind?(enum): [build, learn, personal]
  target_date?: string, ISO date when known
  why?: string, why the user wants it
  link?(array): string, related URLs such as a repository or course page
  with?(array): Person, who else is involved
settings:
  validation: warn
---

Something the user is working towards over time: a project they are
building (kind build), something they are learning (kind learn) or another
personal aim (kind personal). Save in folder goals/ with note_type goal,
titled with the goal itself. Progress updates and tasks are separate notes
that link to the goal, so the goal note holds only what defines it; update
its status when that changes.
