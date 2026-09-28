---
id: RR-TDWCZX
type: review-response
title: ID minting should retry on conflict, not hold a Tx
finding: CreateEntity is already atomic on the ID (ErrConflict). collectAllIDs scans every entity; inside RELW it would block every create and delete Tx in the schema.
severity: significant
resolution: Generated-ID creates retry on ErrConflict (bounded), no Tx. Tx only wraps the unique check + write, and only when the type declares unique properties.
status: addressed
---
