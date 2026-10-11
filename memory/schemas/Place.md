---
title: Place
type: schema
entity: place
version: 1
schema:
  kind: string, e.g. home, office, restaurant, city
  location?: string, full address, or city when that is all that is known
  coordinates?: string, WGS84 decimal degrees "lat,lon", e.g. 12.9719,77.6412
settings:
  validation: warn
---

A place that matters to the user, including workplaces. Save in folder
places/ with note_type place. Save coordinates only when the user gives them
(e.g. from a maps link); never look them up or guess them.
