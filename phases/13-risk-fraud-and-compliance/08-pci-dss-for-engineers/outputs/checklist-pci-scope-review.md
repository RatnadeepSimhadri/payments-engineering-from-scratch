---
name: checklist-pci-scope-review
description: Design-review checklist for deciding and defending the PCI DSS scope of a system that accepts cards, with the questions a QSA will ask
phase: 13
lesson: 8
tags: [pci-dss, scope, cde, tokenization, review]
---

# PCI DSS Scope Review Checklist

Run this before the first line of checkout code is written, and again before every annual attestation. The cheapest control is the one you do not need; every box in section 1 that is ticked removes boxes from sections 2 through 5.

## 1. Can the PAN avoid our systems entirely?

- [ ] Card capture uses the provider's hosted page or hosted fields (iframe). The PAN never enters our DOM, our origin's JavaScript, or our backend.
- [ ] If our JavaScript must handle the PAN (direct post), the decision is documented with the reason, and we accept SAQ A-EP.
- [ ] If our backend must receive the PAN, the decision is signed off by the person who owns the compliance budget, and we accept SAQ D or a QSA assessment.
- [ ] Card-present acceptance uses validated P2PE terminals.
- [ ] Phone and mail orders use the provider's virtual terminal or pay-by-link, not an internal tool that receives the PAN.
- [ ] Recurring charges, refunds, and disputes use the provider's token; no flow requires the PAN after the first authorization.

## 2. Where is the boundary?

- [ ] A current network diagram marks the CDE, connected-to systems, and out-of-scope systems, with every allowed flow between them.
- [ ] A data-flow diagram traces the PAN from entry to the provider and shows every system it passes through, including load balancers, proxies, and WAFs.
- [ ] Default deny applies in both directions at the CDE boundary; egress from the CDE is restricted to the provider's endpoints and named management hosts.
- [ ] Segmentation has been penetration-tested within the last twelve months, and the report is on file.
- [ ] Every system that authenticates, patches, monitors, or logs for the CDE is listed as in scope.

## 3. Does the PAN leak out of the designed path?

- [ ] Request bodies are never logged on payment paths; the logging library's default body capture is disabled explicitly.
- [ ] The error tracker's SDK scrubs card fields by allow-list (fields permitted to be sent), not by deny-list.
- [ ] Session replay and analytics scripts are absent from payment pages, or configured to exclude the card form, with the configuration under change control.
- [ ] No payment form uses `GET`; no redirect carries CHD in a URL; `Referrer-Policy` is set on payment pages.
- [ ] A Luhn-validating PAN detector runs on the log pipeline and alerts on any match.
- [ ] Backups, replicas, and data-warehouse feeds have been scanned for PANs from before tokenization was introduced.
- [ ] Staging and test environments use the provider's synthetic test cards only; production CHD is never copied.
- [ ] Support and admin screens show at most the first six and last four digits, and the full PAN only with a documented business need and an access log.

## 4. Payment page integrity (v4.0 6.4.3 and 11.6.1)

- [ ] An inventory lists every script loaded on pages that capture or embed card capture, with a business justification for each.
- [ ] A Content Security Policy restricts script sources on those pages; third-party scripts carry Subresource Integrity hashes.
- [ ] No tag manager or marketing-controlled script injection exists on payment pages.
- [ ] A change-detection mechanism monitors the payment page's scripts and security headers and alerts on unauthorized change.

## 5. If the PAN must be stored

- [ ] The business reason is written down and reviewed annually; retention has a defined period and automated deletion.
- [ ] PANs are stored only in a vault service with its own audit log; application servers cannot decrypt.
- [ ] A key hierarchy exists: DEKs encrypt data, KEKs encrypt DEKs, KEKs live in an HSM or cloud KMS; custodians, rotation, and retirement are documented.
- [ ] Any hashed PAN uses a keyed hash with managed keys; no unsalted hashes anywhere.
- [ ] SAD (CVV, PIN, track data) is not stored after authorization in any form, and a data-discovery scan confirms it.

## 6. Questions the QSA will ask

- Show me the data-flow diagram. Where does the PAN first appear, and where does it last appear?
- Which SAQ did you select, and what about your card-capture method makes you eligible for it?
- When was segmentation last tested, and by whom?
- How do you know there are no PANs in your logs? Show me the detector and its alert history.
- List everyone who can read a PAN in the clear. Why does each of them need to?
- What third-party scripts run on your checkout page, and how would you know if one changed?
- Show me the MFA configuration for access into the CDE from inside the office network.
- Where are your encryption keys, who are the custodians, and when did they last rotate?
