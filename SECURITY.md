# Security

This module moves money on a ledger, so please report a vulnerability privately rather than in
a public issue.

## Reporting

Use GitHub's private reporting: on the repository's **Security** tab choose **Report a
vulnerability**
(<https://github.com/diyaa-hamza/wallet-holds/security/advisories/new>).

Include the version or commit, the operation involved, and the steps or request that show the
problem. You should get a first answer within a week.

## Supported versions

Fixes land on `main` and in the latest tagged release.

## Scope

In scope: anything that lets a caller book money twice, spend more than a wallet or hold has,
capture or release another wallet's hold, or bypass an overdraft policy.

Out of scope: the HTTP API has no authentication of its own. It is meant to run behind your
own service or gateway, and exposing `walletd` directly to an untrusted network is a
deployment mistake rather than a vulnerability in this module.
