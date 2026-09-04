# Acting-as-user exchange orchestration

- **Acceptance plan:** [`docs/acceptance/acting-as-user-exchange.md`](../../../docs/acceptance/acting-as-user-exchange.md)
- **ADR:** [`docs/adr/0253-acting-as-user-exchange.md`](../../../docs/adr/0253-acting-as-user-exchange.md)
- **Accumulator:** `acc/acting-as-user-exchange`, based on `acc/logical-agent-identity-projection`

Tasks proceed in security-boundary order: closed input/verification values, authority conjunction, output profile, then leakage and adversarial proofs. I3-T, B1, B4, and I3-S remain explicitly out of scope.
