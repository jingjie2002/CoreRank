# Security policy

CoreRank is a local demonstration project and must not be exposed directly to
the public internet. Docker Compose ports bind to `127.0.0.1` by default.

For local shared environments, set `CORERANK_API_KEY` and send it through the
`X-CoreRank-API-Key` header. Replace all passwords from `.env.example`; do not
commit `.env`, credentials, production data, or player data.

To report a vulnerability, open a private security advisory in the repository
instead of a public issue. Include the affected revision, reproduction steps,
impact, and any suggested mitigation. Please do not include real credentials or
personal data in the report.

The project does not currently claim production-grade authentication, mTLS,
multi-tenant isolation, Redis Cluster support, or automated failover.
