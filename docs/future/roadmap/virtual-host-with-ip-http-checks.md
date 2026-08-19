---
title: virtual host with ip http checks
state: evaluating
created: 2026-08-19
tags: [feature, spike]
milestone: v0.1.x
---

i have a name (`chat.quigley.com`) that resolves to a local network address inside HQ. i need to be able to validate (from inside HQ) that the name is functional for external users... if i could specify the IP address and `Host` header, i could express that external check.

or something else that provides the equivalent... but i need to be able to check the external listener.
