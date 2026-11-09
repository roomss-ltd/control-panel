## M1 Write the ledger
- [~] Write ledger.txt @Worker
  - kind: build
  - covers: D1
  - proof: log: the check log shows ledger.txt: ok
- [?] Pick the date format @Worker
  - ask: ISO 8601 or day-month-year?
  - covers: D2

## M2 Check it
- [ ] Check ledger.txt @Reviewer
  - after: 1.1
  - covers: D1, D2
  - due: 2026-01-01 00:00
