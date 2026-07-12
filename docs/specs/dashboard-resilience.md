# Dashboard loading resilience

## Problem

Replacing the embedded frontend binary removes the previous build's hashed assets. Tabs that are still running the previous entry bundle can then request an old lazy-loaded Dashboard chunk and receive a 404. Separately, the user and admin Dashboard views render no persistent state when their initial data request fails, and the Ops Dashboard can dereference a cleared `AbortController` after an asynchronous redirect or unmount.

## Acceptance criteria

1. A missing static asset returns 404 with headers that prevent browser/CDN negative caching.
2. Files retained in `data/public` can be served even when they are not part of the current embedded build, so deployments can retain previous hashed assets.
3. Vite preload errors and Vue Router chunk-load errors share a guarded one-shot reload path.
4. The user Dashboard:
   - requests dashboard statistics independently from refreshing the user profile;
   - renders a persistent failure state with a retry action when no statistics are available;
   - preserves already-rendered statistics when a refresh fails.
5. The admin Dashboard renders a persistent failure state with a retry action on initial snapshot failure and preserves existing data on refresh failure.
6. The admin payment Dashboard renders the same persistent retry state and preserves existing data on refresh failure.
7. The Ops Dashboard uses a request-local abort signal, invalidates stale requests during abort/unmount, and does not fan out legacy fallback requests for cancellation, authentication, or disabled-feature errors.
8. Regression tests cover each failure and recovery path.

## Out of scope

- Usage-log write deadlocks.
- User dashboard aggregation/cache redesign.
- Changing billing or usage accounting semantics.
