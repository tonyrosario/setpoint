# Multi-user designed-for from day one, enforcement deferred

Every API write carries an **Actor** identity from day one, every resource records its owner in metadata, and neither core nor Portal may assume a single user — but authentication and authorization enforcement (OIDC, RBAC) is a deferred milestone, not initial scope. Threading identity through an API after the fact is the expensive part; checking it at the door is cheap to add later. The OIDC design work in `aws-s3-self-service`'s ADRs transfers when that milestone arrives (ADR-0009).
