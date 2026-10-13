---
name: checklist-tokenization-review
description: Design-review checklist for any system that proposes to store, tokenize, or detokenize card numbers
phase: 11
lesson: 5
tags: [pci-dss, tokenization, vault, encryption, scope]
---

# Tokenization Design Review Checklist

Bring this to the first architecture review of any card-on-file, recurring billing, or "save my card" feature, and to any change in a token vault. Work top to bottom: an early "no" usually removes the need for everything after it.

## Do you need the PAN at all?

- [ ] Each consumer of card data is listed with what it actually needs: last four, brand, "same card as before", or the real number.
- [ ] Every consumer except the authorization path is satisfied by a token, last four, or brand.
- [ ] A hosted field or PSP iframe (SAQ A) was considered before building a vault, and the reason for rejecting it is written down.
- [ ] Network tokens (VTS, MDES) were considered for card-on-file, since they survive card reissuance.

## Scope boundary

- [ ] The proposed cardholder data environment is drawn: where the PAN is typed, stored, and transmitted.
- [ ] Logs, traces, APM payloads, heap dumps, message queues, and backups on the PAN path are listed and in scope.
- [ ] Every system outside the boundary receives tokens only, and a test or DLP scan proves no PAN-shaped, Luhn-valid string reaches them.
- [ ] Network segmentation between the CDE and everything else is specified, not assumed.

## Token format

- [ ] The token type is named: reversible random (vault lookup), reversible cryptographic (FPE), or irreversible (keyed hash).
- [ ] If format-preserving, tokens keep length and last four but fail Luhn by construction, so they can never be mistaken for a PAN.
- [ ] Token generation uses a CSPRNG in production and checks for collisions before returning.
- [ ] The same PAN returns the same token for the same merchant (or the reason it should not is documented).

## Storage and cryptography

- [ ] The PAN is stored only as AEAD ciphertext (AES-256-GCM or equivalent), never as plaintext or unauthenticated encryption.
- [ ] The token (or record ID) is bound as associated data, so a ciphertext copied to another row fails authentication.
- [ ] Nonces are unique per key: counter-based or random with a documented bound and a rotation trigger well below it.
- [ ] The nonce counter survives restarts or the key rotates on restart.
- [ ] Any PAN index uses a keyed hash (HMAC) under a key separate from the data key; no unkeyed SHA of a PAN exists anywhere.
- [ ] Separate tenants or merchants use separate index keys unless cross-merchant matching is a stated requirement.

## Key management

- [ ] Data keys live in an HSM or KMS; application memory never holds a master key.
- [ ] Each record stores its key version, and a rotation procedure re-encrypts records without downtime.
- [ ] Key access is logged by the KMS and reviewed.
- [ ] Key destruction is defined for decommissioned tenants.

## Detokenization

- [ ] DETOKENIZE scope is held by the authorization gateway only, by name.
- [ ] Every call carries a purpose string, and unexpected purposes are alerted on.
- [ ] Per-principal rate limits exist, and a burst of detokenizations pages someone.
- [ ] Checks run in order: scope, existence, status, expiry; each denial is audited before the error is returned.
- [ ] The detokenized value is a masked type whose default string form is the mask; raw digits require an explicit call that reviewers grep for.

## Lifecycle

- [ ] Tokens have an expiry aligned with the card-on-file agreement and the retention policy.
- [ ] Suspend and reactivate exist for disputes and locked accounts.
- [ ] Revoke crypto-shreds the ciphertext and keeps a tombstone for the audit trail.
- [ ] Backups that still contain shredded ciphertext age out within the documented retention window.

## Logging and audit

- [ ] Audit entries contain principal, operation, token, decision, reason, and time; never the PAN.
- [ ] Exception messages and validation errors print at most the first six and last four digits.
- [ ] Audit logs are append-only and retained for at least the PCI-required year.

## Tests before go-live

- [ ] A thousand minted tokens are all distinct and all Luhn-invalid.
- [ ] Moved ciphertext, flipped ciphertext bit, and wrong key each fail authentication.
- [ ] A principal without DETOKENIZE scope is denied and audited.
- [ ] Expired, suspended, and revoked tokens are refused.
- [ ] A string search of every log produced by the test suite finds no full PAN.
