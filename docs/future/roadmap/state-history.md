---
title: state history
state: building
created: 2026-08-09
tags: [epic, spike]
milestone: v0.1.x
---

as much as we want to keep things simple... we're going to need to track the state history of the checks we currently have. it's not enough to just see the current state and the time of the last transition... i need to see states over time (of the last year? 5 years?)

we're going to want a simple database... sqlite? and we're going to want to surface transitions over time on the web ui.