# Swedavia schedules and reported flight status

Export a subscription key from the [Swedavia Developer Portal](https://apideveloper.swedavia.se/):

```sh
export SWEDAVIA_API_KEY='your-subscription-key'
./aerolatch departures ARN --date 2026-10-21
./aerolatch arrivals ARN --date 2026-10-21 --json
./aerolatch track D84487 --origin ARN --date 2026-10-21 --tui
```

## Automatic coverage

With the key set, `track` automatically discovers airport information when an
explicit origin/destination or reference route touches a Swedavia airport:
**ARN, BMA, GOT, MMX, LLA, UME, OSD, VBY, RNB, KRN**. ICAO equivalents are also
recognized. `--origin ARN` selects departures; an explicit covered `--destination`
selects arrivals; otherwise a covered reference destination is preferred, then
the reference origin. Coverage depends on airports, not airline.

Use `--swedavia off` to opt out for a live-tracking invocation. Without a key,
ordinary live tracking works as before. Flight-board commands and explicit dated
tracking require a key.

## Dates and boards

`departures AIRPORT` and `arrivals AIRPORT` accept `--date`, `--json`, `--timeout`,
and `--all`. Dates mean the **local calendar day at the Swedish airport**, in
Europe/Stockholm; the default is today. The client fetches the two overlapping
UTC-day boards, filters by scheduled time, and handles summer/winter clock
changes. Text boards show both local scheduled time and UTC. Deleted records
(`DEL`) are hidden unless `--all`; cancellations remain visible.

The API advertises roughly 90 days forward. Coverage varies, and historical
requests can fail even within the documented look-back window. The CLI reports
these failures rather than silently substituting another date.

## Selecting a leg and acquiring its aircraft

Matching uses operating flight number, codeshares, callsign, explicit route hints,
and available aircraft assignment. Leading flight-number zeroes and registrations
such as `SERTG`/`SE-RTG` are normalized for matching. Multiple matching legs remain
ambiguous rather than being chosen arbitrarily. Once selected, operating ID, UTC
departure day, and route pin the leg across refreshes; another service reusing the
callsign cannot replace it.

A dated Swedavia route takes precedence over static reference data for estimates;
explicit destination overrides retain priority. If both endpoints are covered,
matching departure and arrival records are combined. Departure-only coverage does
not guarantee a landing report at a foreign destination.

Before acquisition, changed callsign/aircraft assignments are refreshed from the
selected leg. For example, D84487 can resolve to the operational callsign NSZ87C.
When a departure time is available, live acquisition opens **30 minutes before
scheduled departure**, or a later estimated departure. Arrival-only records supply
a UTC departure day, not an exact takeoff time, so acquisition is gated by that day;
if absent, the fallback opens 24 hours before scheduled arrival. Available assigned
registration is checked before accepting an aircraft, then normal hex locking
takes over. Airport assignments can be missing or provisional before departure.

An explicit date requires a fresh, unique match. An airport outage cannot release
a selected future or finished callsign into an unrelated live lookup. `--date`
accepts flight number, callsign, or registration selection; direct hex mode is
physical-aircraft tracking and does not accept dated flight selection.

The process must remain running to wait. This is an in-process scheduled watcher,
not a persistent background watchlist. `--once --json` can inspect a future service
without waiting. `tracking.live_lookup_performed` distinguishes a live request
from a planned/paused lookup key; waiting does not query ADS-B.

## Reported status versus observed evidence

Airport status is separate from ADS-B telemetry and inferred arrival assessments:

```text
Airport: LANDED (Swedavia)
Arrival actual: 05:11:54Z
Inferred: UNKNOWN
```

Here the airport reported arrival, while no ADS-B landing sequence was observed.
It does not fabricate a surface report or position. Actual/estimated structured
times are displayed in UTC; human status text can contain local times and is not
parsed for timestamps.

- `CAN` is cancelled, `LAN` landed, and `DEL` deleted (not cancelled).
- Terminal airport states hold unacquired flight-number/callsign sessions rather
  than acquiring a reused callsign. Already locked aircraft and explicit physical
  aircraft tracking can continue to supply positions.
- Reported terminal states suppress a new touchdown forecast. Reported diversion
  or reroute suspends previous-destination inference until context is verified.
- Assignment mismatch is `identity_conflict`; that airport status is not applied
  to the locked aircraft.
- A cached record retained after failure is clearly labeled as a previous/stale
  report. A bound aircraft can continue live tracking and independent inference.

The dashboard displays the source, reported time, inferred assessment, and data
age separately; `?` includes route, date, callsign, assignment, gate/terminal,
diagnostics, and next airport refresh.

## Quota, cache, and credentials

Airport refresh is independent of `--interval`: normally five minutes near the
flight, 15 minutes several hours ahead, one hour for distant schedules and landed/
deleted records, and 30 minutes for unmatched/ambiguous searches. Cancelled records
refresh every 15 minutes. An unchanged aircraft does not trigger extra airport
refreshes. UTC-day data and failures are cached in memory per process, up to 32
entries. There is no persistent cache; separate invocations have separate budgets.
Local-day listing typically makes two requests; a second covered airport can
require more. The free plan advertises 10,000 requests/month.

HTTP 401/403 disables further airport requests until restart. HTTP 429 honors a
cooldown/Retry-After. Credentials are only sent in the subscription header, never
URLs or output; authenticated redirects are not followed. Failed response bodies
are not included in diagnostics. Airport errors without a selected dated leg fall
back to the existing live-data workflow.

The API's global Last-Modified is exposed as `data_updated_at`; it is not a
per-flight update time. `fetched_at` is when the record was actually obtained, not
the time of every subsequent cache read.

## JSON fields

`airport_report` contains board airport/direction/local date, `match`, selected
`flight`, `stale`, `error`, and `next_check_at`. The flight includes route,
departure/arrival scheduled/estimated/reported-actual times, status code/text,
codeshares, callsign, optional registration, gate/terminal, and source timestamps.
It is independent of `aircraft` and `arrival_assessment`.

`route_source` identifies Swedavia versus the adsbdb reference. The original
reference can remain at the top level alongside the dated route in
`airport_report.flight`. Destination `source` can be `swedavia`, while an explicit
override remains `user_override`.

Additional top-level tracking states are `scheduled`, `waiting_for_schedule`,
`cancelled`, `reported_landed`, and `deleted`. They do not imply live telemetry.
The selected API's reported actual time is not independently asserted as an exact
ADS-B-observed touchdown time.

Flight data from [Swedavia](https://www.swedavia.se), subject to its API terms.
