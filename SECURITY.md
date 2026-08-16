# Security policy

## Scope

This application is designed to run on your own machine, for one user, without exchange credentials.
It places no orders, holds no API keys with trading permissions, and reaches only public endpoints.
That removes the worst class of failure, but not all of them.

Issues worth reporting include: SSRF or request forgery through the news collector or any
user-supplied URL, injection into the SQL layer, a path where unvalidated model output reaches
execution or the user as a signal, a leak of `.env` values into logs or API responses, and anything
that lets a page loaded in the UI act on the backend on your behalf.

Out of scope: losing money on a trade you placed, the accuracy of any prediction, and vulnerabilities
in the model weights or in llama.cpp itself (report those upstream).

## Reporting

Please use GitHub's **private vulnerability reporting** (Security → Report a vulnerability) rather
than a public issue, and include:

* what an attacker can achieve, and what access they need to start;
* the smallest reproduction you have;
* the commit or version you tested.

Expect an acknowledgement within a few days. Because this is a volunteer project, a fix timeline
depends on severity and availability — you will be told which. Please give a reasonable window
before public disclosure, and credit is offered in the release notes unless you prefer otherwise.

## Hardening notes for operators

* Keep `.env` out of version control (it is git-ignored) and off shared machines. It holds database
  credentials and any API keys you add.
* The compose stack binds to localhost by default. If you expose the UI or the API on a network,
  put an authenticating reverse proxy in front — the application itself has no user accounts.
* News sources are user-editable URLs. Private and loopback addresses are blocked, including through
  redirects and at dial time; leave `NEWS_ALLOW_PRIVATE_FEEDS=false` unless you understand the
  consequence.
* An OpenAI-compatible endpoint you point the backend at receives your market snapshots. With the
  bundled llama.cpp profile nothing leaves the machine; with a remote endpoint, that data does.
