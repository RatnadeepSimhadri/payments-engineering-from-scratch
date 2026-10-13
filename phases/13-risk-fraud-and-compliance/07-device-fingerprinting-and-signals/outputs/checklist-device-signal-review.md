---
name: checklist-device-signal-review
description: Review checklist for adding a device, behavioral, or network signal to fraud rules or scores, covering spoofability, error rates, and privacy basis
phase: 13
lesson: 7
tags: [fraud, device-fingerprinting, behavioral-biometrics, ip-intelligence, privacy]
---

# Device and Behavioral Signal Review

Fill in one copy per new signal before it reaches a rule or a model.

## The signal
- [ ] Kind: stored identifier, derived fingerprint, attestation, behavioral, or network
- [ ] Who controls it: client code, server observation, or platform hardware
- [ ] Measured joint frequency on your own traffic (not published entropy), by platform (desktop, iOS, Android)
- [ ] Stability: how often it changes for the same legitimate device (browser updates, reinstalls, travel)

## Error rates
- [ ] Legitimate populations that trigger it named (new phones, VPN users, CGNAT mobile, assistive-technology users, travellers)
- [ ] False-positive rate measured on labelled good traffic for the intended action
- [ ] Known bypass and the attacker's cost (antidetect browser, residential proxy, emulator)
- [ ] Consistency checks it participates in (UA vs TLS, timezone vs IP, attestation vs claimed platform)

## Use
- [ ] Used as a weighted feature or step-up trigger, not a sole decline reason
- [ ] Thresholds set per segment where populations differ (connection type, platform)
- [ ] Explanation text for analysts and reason codes for decisions defined

## Privacy and policy
- [ ] Purpose stated as fraud prevention for a user-initiated action; ePrivacy Art. 5(3) strict-necessity or consent position recorded
- [ ] GDPR legitimate-interest assessment done; biometric-data question answered for behavioral signals
- [ ] Platform policy checked (App Store required-reason APIs, Play policies)
- [ ] Retention period set; no export to marketing or analytics; access limited to the fraud service
