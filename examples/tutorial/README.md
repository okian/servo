# servoorders

The reference implementation for the servo microservice tutorial — start at
[`docs/tutorial/README.md`](../../docs/tutorial/README.md), not here. This module exists to be
read alongside those chapters; on its own, this file only covers running what's already built.

```
make up               # start Postgres, Redis, NATS
make generate         # run servo generate
make run              # start the service
make test             # unit tests (no services needed)
make test-integration # unit + integration tests against the services from `make up`
make down             # stop and remove the services
```
