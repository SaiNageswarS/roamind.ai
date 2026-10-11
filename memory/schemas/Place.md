---
title: Place
type: schema
entity: place
version: 1
schema:
  kind: string, the type of place
  location?: string, full address, or city when that is all that is known
  coordinates?: string, WGS84 decimal degrees written latitude,longitude
settings:
  validation: warn
---

A place that matters to the user, including workplaces. Save in folder
places/ with note_type place. Save coordinates only when the user gives them
(a maps link counts); never look them up or guess them.
