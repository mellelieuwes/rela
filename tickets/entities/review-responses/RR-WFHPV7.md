---
id: RR-WFHPV7
type: review-response
title: confirm accepts other scalars as text
finding: 'confirm: 1 or an unquoted confirm: yes decoded as dialog text.'
severity: nit
resolution: UnmarshalYAML accepts only !!bool or !!str and refuses unquoted YAML 1.1 boolean words with a hint to quote them. Tests extended; no shipped config affected.
status: addressed
---
