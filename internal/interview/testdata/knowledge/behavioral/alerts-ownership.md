---
id: alerts-ownership
title: Cutting alert noise before launch
principles: [ownership, delivery]
story_source_type: work
dimensions: [metric, tradeoff]
triggers: []
allowed_facts:
  - "Cut 200 alerts to 15 alerts in 12 minutes"
---

## Situation
The on-call pager fired all night for the backend services at Example Systems.

## Task
I owned the backend services end to end and had to cut the alert noise before the launch deadline.

## Action
I decided to group alerts by cause because the risk of missing a real outage was higher than one tuning week.

## Result
I cut 200 alerts to 15 alerts and the median page time fell to 12 minutes.
