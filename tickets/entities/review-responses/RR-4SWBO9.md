---
id: RR-4SWBO9
type: review-response
title: Two server processes can both apply one suggestion
finding: writeMu serializes one process only. Two rela-server nodes on one database could both read the comment as open and both splice the replacement.
severity: critical
resolution: The accept now claims the comment with a conditional SetResolved (WHERE resolved <> $3 on pg and sqlite; locked read-modify-write on file and mem). Only the winner writes the body. Contract test with 8 concurrent racers on all four backends.
status: addressed
---
